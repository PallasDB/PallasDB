package db

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
)

// fileLike is the subset of *os.File that the write-ahead log and the metadata
// store need. Both reach their files through openFileSync below so that tests
// can wrap the real file in a fault injector (see fault_test.go); SortedFile
// deliberately keeps using *os.File directly.
type fileLike interface {
	io.ReaderAt
	io.WriterAt
	Stat() (os.FileInfo, error)
	Sync() error
	Truncate(size int64) error
	Close() error
}

// openFileSync creates or opens a file whose directory entry has been made
// durable. It is a variable only so that tests can intercept it; nothing in
// production code reassigns it.
var openFileSync = func(name string) (fileLike, error) {
	fp, err := createFileSync(name)
	if err != nil {
		return nil, err // never hand back a typed-nil inside the interface
	}
	return fp, nil
}

// Log is the write-ahead log: a single file that is reset in place by Truncate
// at the end of every memtable flush.
//
// An in-place reset is where naive WAL designs lose data. Truncation is not
// atomic with respect to a crash, and every record carries its own valid CRC32,
// so if the truncate does not reach stable storage and new records are then
// written at the front of the file, replay happily walks off the end of the new
// records straight into the old ones -- including an old EntryCommit. Deleted
// keys come back and superseded values win. Two mechanisms prevent that here:
//
//  1. Every record carries a sequence number that is monotonic over the entire
//     lifetime of the log. Truncate does not reset it.
//  2. The log header holds `epoch`, a durable floor on those sequence numbers.
//     Truncate raises the floor to the last sequence number written and fsyncs
//     the header *before* shrinking the file.
//
// Replay accepts a record only if its sequence number is strictly greater than
// everything accepted so far, starting from the floor. Records from before a
// truncate are therefore unreachable whether or not the truncate itself
// survived, and a stale record exposed inside the current generation (by a
// shorter transaction overwriting a longer one) is rejected too.
//
// The floor lives in the log header rather than in the metadata file because it
// must be raised inside the same critical section as the truncate. The metadata
// store is written earlier in a flush, before the log is reset, so putting the
// floor there would either order it wrongly or cost a second fsync of an
// unrelated file on every flush. Keeping it in the log makes Truncate a
// self-contained durability operation.
type Log struct {
	FileName string
	fp       fileLike
	reader   OffsetReader
	// epoch is the durable floor on record sequence numbers, read from and
	// written to the log header.
	epoch uint64
	// seq is the highest sequence number written or replayed so far.
	seq    uint64
	writer struct {
		offset    int64
		committed int64
	}
}

// Log header layout: magic (8) | epoch (8) | crc32 of the preceding 16 bytes (4).
const logHeaderSize = 8 + 8 + 4

var logMagic = [8]byte{'P', 'D', 'B', 'W', 'A', 'L', '1', '\n'}

// ErrLogHeaderCorrupt reports a log file that has content but no readable
// header. Recovery cannot know the sequence floor in that case, so opening
// fails loudly instead of guessing and risking a resurrection.
var ErrLogHeaderCorrupt = errors.New("write-ahead log header is corrupt")

func (log *Log) Open() (err error) {
	fp, err := openFileSync(log.FileName)
	if err != nil {
		return err
	}
	log.fp = fp
	if err = log.openHeader(); err != nil {
		_ = log.fp.Close()
		log.fp = nil
		return fmt.Errorf("open %s: %w", log.FileName, err)
	}
	log.reader = OffsetReader{log.fp, logHeaderSize}
	log.writer.offset = logHeaderSize
	log.writer.committed = logHeaderSize
	log.seq = log.epoch
	return nil
}

func (log *Log) openHeader() error {
	var buf [logHeaderSize]byte
	n, err := log.fp.ReadAt(buf[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if n < logHeaderSize {
		// Fresh, or a create that was interrupted before the header landed.
		// Either way no complete record can exist yet, so a zero floor is safe.
		return log.writeHeader(0)
	}
	if [8]byte(buf[0:8]) != logMagic {
		return fmt.Errorf("%w: bad magic", ErrLogHeaderCorrupt)
	}
	if binary.LittleEndian.Uint32(buf[16:20]) != crc32.ChecksumIEEE(buf[0:16]) {
		return fmt.Errorf("%w: checksum mismatch", ErrLogHeaderCorrupt)
	}
	log.epoch = binary.LittleEndian.Uint64(buf[8:16])
	return nil
}

// writeHeader raises the durable sequence floor. It returns only once the new
// floor has reached stable storage.
func (log *Log) writeHeader(epoch uint64) error {
	var buf [logHeaderSize]byte
	copy(buf[0:8], logMagic[:])
	binary.LittleEndian.PutUint64(buf[8:16], epoch)
	binary.LittleEndian.PutUint32(buf[16:20], crc32.ChecksumIEEE(buf[0:16]))
	if _, err := log.fp.WriteAt(buf[:], 0); err != nil {
		return err
	}
	if err := log.fp.Sync(); err != nil {
		return err
	}
	log.epoch = epoch
	return nil
}

func (log *Log) Close() error {
	if log.fp == nil {
		return nil
	}
	err := log.fp.Close()
	log.fp = nil
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func (log *Log) Write(ent *Entry) error {
	ent.seq = log.seq + 1
	data, err := ent.Encode()
	if err != nil {
		return err
	}
	if _, err := log.fp.WriteAt(data, log.writer.offset); err != nil {
		return err
	}
	log.writer.offset += int64(len(data))
	log.seq = ent.seq
	return nil
}

func (log *Log) Commit() error {
	if err := log.Write(&Entry{op: EntryCommit}); err != nil {
		return err
	}
	if err := log.fp.Sync(); err != nil {
		return err
	}
	log.writer.committed = log.writer.offset
	return nil
}

// ResetTX rewinds the write cursor to the last commit, discarding the records
// of an aborted transaction. It deliberately does not rewind log.seq: the next
// transaction overwrites those bytes and must not reuse their sequence numbers,
// or a leftover tail could be mistaken for live data during replay.
func (log *Log) ResetTX() {
	log.writer.offset = log.writer.committed
}

func (log *Log) Read(ent *Entry) (eof bool, err error) {
	err = ent.Decode(&log.reader)
	if err != nil {
		// A short read, a bad checksum, a garbage length or a garbage opcode
		// all mean the same thing: the current generation of the log ends here.
		// None of them may fail Open, or a single damaged tail would brick the
		// store permanently.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, ErrBadSum) || errors.Is(err, ErrEntryTooLarge) ||
			errors.Is(err, ErrBadEntryOp) {
			return true, log.endReplay()
		}
		return false, err
	}
	if ent.seq <= log.seq {
		// Left over from a previous generation (a truncate that did not reach
		// disk) or from a longer transaction that a shorter one overwrote.
		return true, log.endReplay()
	}
	log.seq = ent.seq
	if ent.op == EntryCommit {
		log.writer.offset = log.reader.offset
		log.writer.committed = log.reader.offset
	}
	return false, nil
}

// endReplay drops everything past the last commit. The sequence floor already
// makes that tail unreachable; physically removing it means a later short
// transaction can never expose a stale record that happens to land on a record
// boundary.
func (log *Log) endReplay() error {
	st, err := log.fp.Stat()
	if err != nil {
		return err
	}
	if st.Size() == log.writer.committed {
		return nil
	}
	if err := log.fp.Truncate(log.writer.committed); err != nil {
		return err
	}
	return log.fp.Sync()
}

// Truncate resets the log to an empty generation. See the type comment for why
// the header write has to come first and be durable.
func (log *Log) Truncate() error {
	if err := log.writeHeader(log.seq); err != nil {
		return err
	}
	// The log is logically empty from here on, whether or not the file truncate
	// below succeeds: replay rejects everything at or below the new floor.
	// Resetting the cursors unconditionally matters -- continuing to write past
	// a stale region would strand the new records behind a record that replay
	// stops at.
	log.writer.offset = logHeaderSize
	log.writer.committed = logHeaderSize
	if err := log.fp.Truncate(logHeaderSize); err != nil {
		return err
	}
	return log.fp.Sync()
}

type OffsetReader struct {
	inner  io.ReaderAt
	offset int64
}

func (rd *OffsetReader) Read(buf []byte) (n int, err error) {
	n, err = rd.inner.ReadAt(buf, rd.offset)
	if n > 0 {
		err = nil
	}
	rd.offset += int64(n)
	return n, err
}
