package db

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

type EntryOp uint8

const (
	EntryAdd    EntryOp = 0
	EntryDel    EntryOp = 1
	EntryCommit EntryOp = 2
)

// MaxEntrySize caps the combined key and value length of a single WAL record.
//
// The cap is enforced by Encode as well as by Decode. Enforcing it on only one
// side is a trap: a record that the encoder is willing to write but the decoder
// refuses to read cannot be recovered from, so a single oversized value would
// make the store fail to Open forever.
const MaxEntrySize = 64 << 20 // 64 MiB

var (
	// ErrEntryTooLarge is returned when a record exceeds MaxEntrySize, either
	// because the caller tried to write one or because a decoded length field
	// is garbage.
	ErrEntryTooLarge = errors.New("log entry too large")
	// ErrBadSum reports a record whose CRC32 does not match its contents.
	ErrBadSum = errors.New("bad checksum")
	// ErrBadEntryOp reports a record carrying an unknown operation code.
	ErrBadEntryOp = errors.New("invalid log entry op")
)

// A WAL record is laid out as:
//
//	 0..4   crc32 of every byte that follows
//	 4..12  seq, the record's sequence number (see Log for what it guarantees)
//	12..16  key length
//	16..20  value length
//	20..21  operation
//	21..    key bytes, then value bytes
//
// The sequence number is inside the checksummed region on purpose: a record
// cannot be re-stamped with a different generation without invalidating it.
const entryHeaderSize = 4 + 8 + 4 + 4 + 1

type Entry struct {
	key []byte
	val []byte
	// seq is assigned by (*Log).Write; it is monotonic over the whole lifetime
	// of a log file and is never reset, not even by Truncate.
	seq uint64
	op  EntryOp
}

func (ent *Entry) Encode() ([]byte, error) {
	size := len(ent.key) + len(ent.val)
	if size > MaxEntrySize {
		return nil, fmt.Errorf("%w: %d bytes (max %d)", ErrEntryTooLarge, size, MaxEntrySize)
	}
	data := make([]byte, entryHeaderSize+size)
	binary.LittleEndian.PutUint64(data[4:12], ent.seq)
	binary.LittleEndian.PutUint32(data[12:16], uint32(len(ent.key)))
	binary.LittleEndian.PutUint32(data[16:20], uint32(len(ent.val)))
	data[20] = byte(ent.op)
	copy(data[entryHeaderSize:], ent.key)
	copy(data[entryHeaderSize+len(ent.key):], ent.val)
	binary.LittleEndian.PutUint32(data[0:4], crc32.ChecksumIEEE(data[4:]))
	return data, nil
}

func (ent *Entry) Decode(r io.Reader) error {
	var header [entryHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	seq := binary.LittleEndian.Uint64(header[4:12])
	// Widen to uint64 before adding: on a 32-bit platform two u32 lengths
	// overflow int and would turn into a negative make() size.
	klen := uint64(binary.LittleEndian.Uint32(header[12:16]))
	vlen := uint64(binary.LittleEndian.Uint32(header[16:20]))
	if klen+vlen > MaxEntrySize {
		return fmt.Errorf("%w: %d bytes (max %d)", ErrEntryTooLarge, klen+vlen, MaxEntrySize)
	}
	op := EntryOp(header[20])

	data := make([]byte, klen+vlen)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}

	h := crc32.NewIEEE()
	h.Write(header[4:])
	h.Write(data)
	if h.Sum32() != binary.LittleEndian.Uint32(header[0:4]) {
		return ErrBadSum
	}
	switch op {
	case EntryAdd, EntryDel, EntryCommit:
	default:
		return fmt.Errorf("%w: %d", ErrBadEntryOp, op)
	}

	ent.seq = seq
	ent.op = op
	ent.key = data[:klen]
	ent.val = data[klen:]
	return nil
}
