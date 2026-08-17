package cluster

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/db"
)

// Snapshot wire format. Both directions stream: neither side ever holds more
// than one record in memory.
//
//	header:  [4B magic]["PDBS"][2B version LE][2B reserved LE, zero]
//	record:  [1B tag=1][4B klen LE][4B vlen LE][key][val]
//	end:     [1B tag=0]
//	trailer: [4B CRC32C LE of every preceding byte]
//
// Key and value lengths are validated against MaxKeyLen / MaxValueLen before a
// single byte is allocated, and the CRC is verified before the restored data is
// swapped in.
const (
	snapshotVersion uint16 = 1

	snapshotRecordTag byte = 1
	snapshotEndTag    byte = 0

	snapshotHeaderLen  = 8
	snapshotTrailerLen = 4
)

var (
	snapshotMagic = [4]byte{'P', 'D', 'B', 'S'}

	// crc32c matches the checksum used by the storage engine's own files.
	crc32c = crc32.MakeTable(crc32.Castagnoli)

	// ErrSnapshotMagic is returned when a stream is not a PallasDB snapshot.
	ErrSnapshotMagic = errors.New("cluster: not a pallasdb snapshot")
	// ErrSnapshotVersion is returned for a snapshot written by another version.
	ErrSnapshotVersion = errors.New("cluster: unsupported snapshot version")
	// ErrSnapshotChecksum is returned when the CRC32C trailer does not match.
	ErrSnapshotChecksum = errors.New("cluster: snapshot checksum mismatch")
	// ErrSnapshotCorrupt is returned for structurally invalid snapshot data.
	ErrSnapshotCorrupt = errors.New("cluster: corrupt snapshot")
)

// FSMSnapshot implements raft.FSMSnapshot.
// It holds an open transaction providing a point-in-time consistent view.
type FSMSnapshot struct {
	iter    db.SortedKVIter
	cleanup func()
}

// Persist streams every live key-value pair to the raft.SnapshotSink.
func (s *FSMSnapshot) Persist(sink raft.SnapshotSink) error {
	if err := writeSnapshot(sink, s.iter); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

// Release is called after Persist completes (success or failure).
func (s *FSMSnapshot) Release() {
	s.cleanup()
}

// writeSnapshot streams the iterator into w in the snapshot wire format.
func writeSnapshot(w io.Writer, iter db.SortedKVIter) error {
	bw := bufio.NewWriter(w)
	h := crc32.New(crc32c)
	hw := &hashingWriter{w: bw, h: h}

	var hdr [snapshotHeaderLen]byte
	copy(hdr[0:4], snapshotMagic[:])
	binary.LittleEndian.PutUint16(hdr[4:6], snapshotVersion)
	if _, err := hw.Write(hdr[:]); err != nil {
		return fmt.Errorf("snapshot write header: %w", err)
	}

	var rec [9]byte
	rec[0] = snapshotRecordTag
	for iter.Valid() {
		key, val := iter.Key(), iter.Val()
		if len(key) > MaxKeyLen {
			return fmt.Errorf("snapshot key of %d bytes exceeds limit %d", len(key), MaxKeyLen)
		}
		if len(val) > MaxValueLen {
			return fmt.Errorf("snapshot value of %d bytes exceeds limit %d", len(val), MaxValueLen)
		}
		binary.LittleEndian.PutUint32(rec[1:5], uint32(len(key)))
		binary.LittleEndian.PutUint32(rec[5:9], uint32(len(val)))
		if _, err := hw.Write(rec[:]); err != nil {
			return fmt.Errorf("snapshot write record header: %w", err)
		}
		if _, err := hw.Write(key); err != nil {
			return fmt.Errorf("snapshot write key: %w", err)
		}
		if len(val) > 0 {
			if _, err := hw.Write(val); err != nil {
				return fmt.Errorf("snapshot write value: %w", err)
			}
		}
		if err := iter.Next(); err != nil {
			return fmt.Errorf("snapshot iterate: %w", err)
		}
	}

	if _, err := hw.Write([]byte{snapshotEndTag}); err != nil {
		return fmt.Errorf("snapshot write terminator: %w", err)
	}

	// The trailer is written past the hash, not through it.
	var trailer [snapshotTrailerLen]byte
	binary.LittleEndian.PutUint32(trailer[:], h.Sum32())
	if _, err := bw.Write(trailer[:]); err != nil {
		return fmt.Errorf("snapshot write checksum: %w", err)
	}
	return bw.Flush()
}

// readSnapshot streams r, calling visit once per record. The key and value
// handed to visit are freshly allocated and may be retained by the caller.
// The checksum is verified after the last record, so a caller that installs
// data incrementally must not publish it until readSnapshot returns nil.
func readSnapshot(r io.Reader, visit func(key, val []byte) error) error {
	br := bufio.NewReader(r)
	h := crc32.New(crc32c)
	hr := &hashingReader{r: br, h: h}

	var hdr [snapshotHeaderLen]byte
	if _, err := io.ReadFull(hr, hdr[:]); err != nil {
		return fmt.Errorf("snapshot read header: %w", err)
	}
	if string(hdr[0:4]) != string(snapshotMagic[:]) {
		return fmt.Errorf("%w: magic %q", ErrSnapshotMagic, hdr[0:4])
	}
	if v := binary.LittleEndian.Uint16(hdr[4:6]); v != snapshotVersion {
		return fmt.Errorf("%w: %d (want %d)", ErrSnapshotVersion, v, snapshotVersion)
	}

	var rec [9]byte
	for i := uint64(0); ; i++ {
		if _, err := io.ReadFull(hr, rec[:1]); err != nil {
			return fmt.Errorf("snapshot read tag %d: %w", i, err)
		}
		if rec[0] == snapshotEndTag {
			break
		}
		if rec[0] != snapshotRecordTag {
			return fmt.Errorf("%w: bad record tag %d at %d", ErrSnapshotCorrupt, rec[0], i)
		}
		if _, err := io.ReadFull(hr, rec[1:]); err != nil {
			return fmt.Errorf("snapshot read record header %d: %w", i, err)
		}
		klen := binary.LittleEndian.Uint32(rec[1:5])
		vlen := binary.LittleEndian.Uint32(rec[5:9])
		if klen > MaxKeyLen {
			return fmt.Errorf("%w: key length %d at record %d exceeds limit %d", ErrSnapshotCorrupt, klen, i, MaxKeyLen)
		}
		if vlen > MaxValueLen {
			return fmt.Errorf("%w: value length %d at record %d exceeds limit %d", ErrSnapshotCorrupt, vlen, i, MaxValueLen)
		}
		if klen == 0 {
			return fmt.Errorf("%w: zero-length key at record %d", ErrSnapshotCorrupt, i)
		}

		// One allocation per record, sliced into key and value.
		buf := make([]byte, int(klen)+int(vlen))
		if _, err := io.ReadFull(hr, buf); err != nil {
			return fmt.Errorf("snapshot read record %d: %w", i, err)
		}
		if err := visit(buf[:klen:klen], buf[klen:]); err != nil {
			return err
		}
	}

	want := h.Sum32()
	var trailer [snapshotTrailerLen]byte
	if _, err := io.ReadFull(br, trailer[:]); err != nil {
		return fmt.Errorf("snapshot read checksum: %w", err)
	}
	if got := binary.LittleEndian.Uint32(trailer[:]); got != want {
		return fmt.Errorf("%w: got %08x, want %08x", ErrSnapshotChecksum, got, want)
	}
	return nil
}

// hashingWriter feeds everything written through it into h.
type hashingWriter struct {
	w io.Writer
	h hash.Hash32
}

func (hw *hashingWriter) Write(p []byte) (int, error) {
	n, err := hw.w.Write(p)
	if n > 0 {
		_, _ = hw.h.Write(p[:n])
	}
	return n, err
}

// hashingReader feeds everything read through it into h.
type hashingReader struct {
	r io.Reader
	h hash.Hash32
}

func (hr *hashingReader) Read(p []byte) (int, error) {
	n, err := hr.r.Read(p)
	if n > 0 {
		_, _ = hr.h.Write(p[:n])
	}
	return n, err
}
