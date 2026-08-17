package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/teddymalhan/pallasdb/client"
	"github.com/teddymalhan/pallasdb/db"
)

// The dump stream is a self-describing, binary-safe backup of a whole keyspace:
//
//	magic    "PALLASDUMP" followed by a one-byte format version
//	record   0x01 | uvarint(len(key)) | key | uvarint(len(value)) | value
//	trailer  0x00 | uvarint(record count) | big-endian uint32 CRC-32C
//
// The CRC covers every byte after the magic up to but excluding the trailer
// tag, so a truncated or corrupted stream is rejected by `restore` instead of
// being silently partially applied.
var dumpMagic = []byte("PALLASDUMP\x01")

const (
	dumpTagRecord byte = 0x01
	dumpTagEnd    byte = 0x00

	// restoreBatchSize bounds how many entries a local restore commits per
	// transaction, trading a little durability granularity for throughput.
	restoreBatchSize = 1000

	// maxDumpEntryBytes rejects absurd length prefixes from a corrupt stream
	// before they are used to size an allocation.
	maxDumpEntryBytes = 1 << 30
)

var errDumpCorrupt = errors.New("dump stream is corrupt")

// dumpWriter serialises entries into the dump stream.
type dumpWriter struct {
	w     *bufio.Writer
	crc   hash.Hash32
	count uint64
	sizeb []byte
}

func newDumpWriter(w io.Writer) (*dumpWriter, error) {
	buffered := bufio.NewWriter(w)
	if _, err := buffered.Write(dumpMagic); err != nil {
		return nil, err
	}
	return &dumpWriter{
		w:     buffered,
		crc:   crc32.New(crc32.MakeTable(crc32.Castagnoli)),
		sizeb: make([]byte, 0, binary.MaxVarintLen64),
	}, nil
}

func (d *dumpWriter) emit(p []byte) error {
	if _, err := d.crc.Write(p); err != nil {
		return err
	}
	_, err := d.w.Write(p)
	return err
}

func (d *dumpWriter) writeEntry(key, value []byte) error {
	d.sizeb = d.sizeb[:0]
	d.sizeb = append(d.sizeb, dumpTagRecord)
	d.sizeb = binary.AppendUvarint(d.sizeb, uint64(len(key)))
	if err := d.emit(d.sizeb); err != nil {
		return err
	}
	if err := d.emit(key); err != nil {
		return err
	}

	d.sizeb = binary.AppendUvarint(d.sizeb[:0], uint64(len(value)))
	if err := d.emit(d.sizeb); err != nil {
		return err
	}
	if err := d.emit(value); err != nil {
		return err
	}
	d.count++
	return nil
}

func (d *dumpWriter) finish() (uint64, error) {
	sum := d.crc.Sum32()

	trailer := append([]byte{dumpTagEnd}, binary.AppendUvarint(nil, d.count)...)
	trailer = binary.BigEndian.AppendUint32(trailer, sum)
	if _, err := d.w.Write(trailer); err != nil {
		return 0, err
	}
	if err := d.w.Flush(); err != nil {
		return 0, err
	}
	return d.count, nil
}

// dumpReader deserialises a dump stream and verifies its trailer.
type dumpReader struct {
	r     *bufio.Reader
	crc   hash.Hash32
	count uint64
	one   [1]byte
}

func newDumpReader(r io.Reader) (*dumpReader, error) {
	buffered := bufio.NewReader(r)
	magic := make([]byte, len(dumpMagic))
	if _, err := io.ReadFull(buffered, magic); err != nil {
		return nil, fmt.Errorf("%w: missing header", errDumpCorrupt)
	}
	if string(magic) != string(dumpMagic) {
		return nil, fmt.Errorf("%w: not a PallasDB dump stream", errDumpCorrupt)
	}
	return &dumpReader{
		r:   buffered,
		crc: crc32.New(crc32.MakeTable(crc32.Castagnoli)),
	}, nil
}

// ReadByte satisfies io.ByteReader for binary.ReadUvarint while keeping the
// running CRC in step with the bytes consumed.
func (d *dumpReader) ReadByte() (byte, error) {
	b, err := d.r.ReadByte()
	if err != nil {
		return 0, err
	}
	d.one[0] = b
	if _, err := d.crc.Write(d.one[:]); err != nil {
		return 0, err
	}
	return b, nil
}

func (d *dumpReader) readN(n uint64) ([]byte, error) {
	if n > maxDumpEntryBytes {
		return nil, fmt.Errorf("%w: entry length %d is implausible", errDumpCorrupt, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(d.r, buf); err != nil {
		return nil, fmt.Errorf("%w: truncated entry", errDumpCorrupt)
	}
	if _, err := d.crc.Write(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// next returns the next entry, or io.EOF once the verified trailer is reached.
func (d *dumpReader) next() (key, value []byte, err error) {
	tag, err := d.r.ReadByte()
	if err != nil {
		return nil, nil, fmt.Errorf("%w: stream ended without a trailer", errDumpCorrupt)
	}

	switch tag {
	case dumpTagEnd:
		return nil, nil, d.verifyTrailer()
	case dumpTagRecord:
		d.one[0] = tag
		if _, err := d.crc.Write(d.one[:]); err != nil {
			return nil, nil, err
		}
	default:
		return nil, nil, fmt.Errorf("%w: unknown record tag %#x", errDumpCorrupt, tag)
	}

	keyLen, err := binary.ReadUvarint(d)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: truncated key length", errDumpCorrupt)
	}
	if key, err = d.readN(keyLen); err != nil {
		return nil, nil, err
	}
	valueLen, err := binary.ReadUvarint(d)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: truncated value length", errDumpCorrupt)
	}
	if value, err = d.readN(valueLen); err != nil {
		return nil, nil, err
	}
	d.count++
	return key, value, nil
}

// verifyTrailer checks the record count and CRC, then reports io.EOF.
func (d *dumpReader) verifyTrailer() error {
	sum := d.crc.Sum32()

	want, err := binary.ReadUvarint(d.r)
	if err != nil {
		return fmt.Errorf("%w: truncated trailer", errDumpCorrupt)
	}
	var checksum [4]byte
	if _, err := io.ReadFull(d.r, checksum[:]); err != nil {
		return fmt.Errorf("%w: truncated checksum", errDumpCorrupt)
	}
	if want != d.count {
		return fmt.Errorf("%w: trailer claims %d entries, read %d", errDumpCorrupt, want, d.count)
	}
	if got := binary.BigEndian.Uint32(checksum[:]); got != sum {
		return fmt.Errorf("%w: checksum mismatch (want %08x, got %08x)", errDumpCorrupt, sum, got)
	}
	return io.EOF
}

// backupOptions selects the source or destination of a dump or restore: a local
// data directory, or a running server.
type backupOptions struct {
	remote  *remoteOptions
	dataDir string
	file    string
}

func addBackupFlags(cmd *cobra.Command, config *configOptions, fileFlag, fileUsage string) *backupOptions {
	opts := &backupOptions{remote: addRemoteFlags(cmd, config)}
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "", "operate directly on a local data directory instead of a server")
	cmd.Flags().StringVar(&opts.file, fileFlag, "", fileUsage)
	cmd.MarkFlagsMutuallyExclusive("addr", "data-dir")
	return opts
}

// local reports whether the command should open a data directory in-process.
func (opts *backupOptions) local() bool { return opts.dataDir != "" }

func newDumpCommand(config *configOptions) *cobra.Command {
	var opts *backupOptions
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Export an entire keyspace as a backup stream",
		Long: "Export an entire keyspace as a binary, checksummed backup stream.\n\n" +
			"By default the stream is read from the server at --addr and written to " +
			"stdout. Pass --data-dir to read a local data directory instead, and " +
			"--output to write to a file.",
		Example: "  pallasdb dump --addr 127.0.0.1:50051 --output backup.pallas\n" +
			"  pallasdb dump --data-dir ./data > backup.pallas",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out, closeOut, err := openDumpOutput(cmd, opts.file)
			if err != nil {
				return err
			}
			defer func() { _ = closeOut() }()

			writer, err := newDumpWriter(out)
			if err != nil {
				return err
			}
			if opts.local() {
				err = dumpLocal(opts.dataDir, writer)
			} else {
				err = dumpRemote(cmd, opts.remote, writer)
			}
			if err != nil {
				return err
			}

			count, err := writer.finish()
			if err != nil {
				return err
			}
			if err := closeOut(); err != nil {
				return err
			}
			// stdout may be carrying the stream, so the summary goes to stderr.
			_, err = fmt.Fprintf(cmd.ErrOrStderr(), "dumped %d keys\n", count)
			return err
		},
	}
	opts = addBackupFlags(cmd, config, "output", "write the dump to this file instead of stdout")
	return cmd
}

func newRestoreCommand(config *configOptions) *cobra.Command {
	var opts *backupOptions
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Import a backup stream produced by dump",
		Long: "Import a backup stream produced by `pallasdb dump`.\n\n" +
			"By default the stream is read from stdin and written to the server at " +
			"--addr. Pass --data-dir to write to a local data directory instead, and " +
			"--input to read from a file. Existing keys are overwritten.",
		Example: "  pallasdb restore --addr 127.0.0.1:50051 --input backup.pallas\n" +
			"  pallasdb restore --data-dir ./data < backup.pallas",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			in, closeIn, err := openRestoreInput(cmd, opts.file)
			if err != nil {
				return err
			}
			defer func() { _ = closeIn() }()

			reader, err := newDumpReader(in)
			if err != nil {
				return err
			}

			var count uint64
			if opts.local() {
				count, err = restoreLocal(opts.dataDir, reader)
			} else {
				count, err = restoreRemote(cmd, opts.remote, reader)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "restored %d keys\n", count)
			return err
		},
	}
	opts = addBackupFlags(cmd, config, "input", "read the dump from this file instead of stdin")
	return cmd
}

func openDumpOutput(cmd *cobra.Command, path string) (io.Writer, func() error, error) {
	if path == "" {
		return cmd.OutOrStdout(), func() error { return nil }, nil
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open output: %w", err)
	}
	closed := false
	return file, func() error {
		if closed {
			return nil
		}
		closed = true
		return file.Close()
	}, nil
}

func openRestoreInput(cmd *cobra.Command, path string) (io.Reader, func() error, error) {
	if path == "" {
		return cmd.InOrStdin(), func() error { return nil }, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open input: %w", err)
	}
	return file, file.Close, nil
}

func dumpLocal(dataDir string, writer *dumpWriter) error {
	store, err := db.NewKV(dataDir)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = store.Close() }()

	iter, release, err := store.IterAll()
	if err != nil {
		return fmt.Errorf("scan database: %w", err)
	}
	defer release()

	for iter.Valid() {
		if err := writer.writeEntry(iter.Key(), iter.Val()); err != nil {
			return err
		}
		if err := iter.Next(); err != nil {
			return fmt.Errorf("scan database: %w", err)
		}
	}
	return nil
}

// dumpRemote streams the whole keyspace out of a server.
//
// A single Range call is not enough: the server caps how many rows one scan may
// return and truncates silently, because the wire format has no "there is more"
// flag. The scan is therefore resumed from the last key received — range bounds
// are inclusive, so that key arrives again and is skipped — until a page adds
// nothing new.
func dumpRemote(cmd *cobra.Command, remote *remoteOptions, writer *dumpWriter) error {
	consistency, err := remote.consistencyLevel()
	if err != nil {
		return err
	}
	return remote.withSessionClient(cmd, func(ctx context.Context, c *client.Client) error {
		var resumeFrom []byte
		for {
			req := client.RangeRequest{Start: resumeFrom, Consistency: consistency}

			var (
				added   uint64
				lastKey []byte
			)
			err := c.Range(ctx, req, func(kv client.KeyValue) error {
				if resumeFrom != nil && bytes.Equal(kv.Key, resumeFrom) {
					return nil // already written by the previous page
				}
				if err := writer.writeEntry(kv.Key, kv.Value); err != nil {
					return err
				}
				added++
				lastKey = kv.Key
				return nil
			})
			if err != nil {
				return err
			}
			if added == 0 {
				return nil
			}
			resumeFrom = bytes.Clone(lastKey)
		}
	})
}

func restoreLocal(dataDir string, reader *dumpReader) (uint64, error) {
	store, err := db.NewKV(dataDir)
	if err != nil {
		return 0, fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = store.Close() }()

	var (
		count   uint64
		pending int
		tx      = store.NewTX()
	)
	defer func() {
		if tx != nil {
			tx.Abort()
		}
	}()

	for {
		key, value, err := reader.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, err
		}
		if _, err := tx.Set(key, value); err != nil {
			return 0, fmt.Errorf("restore key: %w", err)
		}
		count++
		pending++
		if pending >= restoreBatchSize {
			if err := tx.Commit(); err != nil {
				tx = nil
				return 0, fmt.Errorf("commit batch: %w", err)
			}
			tx = store.NewTX()
			pending = 0
		}
	}

	if err := tx.Commit(); err != nil {
		tx = nil
		return 0, fmt.Errorf("commit batch: %w", err)
	}
	tx = nil
	return count, nil
}

func restoreRemote(cmd *cobra.Command, remote *remoteOptions, reader *dumpReader) (uint64, error) {
	var count uint64
	err := remote.withSessionClient(cmd, func(sessionCtx context.Context, c *client.Client) error {
		for {
			key, value, err := reader.next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			putCtx, cancel := remote.callContext(sessionCtx)
			_, err = c.Put(putCtx, key, value, client.PutUpsert)
			cancel()
			if err != nil {
				return fmt.Errorf("restore key: %w", err)
			}
			count++
		}
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}
