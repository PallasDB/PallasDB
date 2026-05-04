package db0903

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

type EntryOp uint8

const (
	EntryAdd    EntryOp = 0
	EntryDel    EntryOp = 1
	EntryCommit EntryOp = 2
)

type Entry struct {
	key []byte
	val []byte
	op  EntryOp
}

func (ent *Entry) Encode() []byte {
	data := make([]byte, 4+4+4+1+len(ent.key)+len(ent.val))
	data[4+4+4] = byte(ent.op)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.key)))
	binary.LittleEndian.PutUint32(data[8:12], uint32(len(ent.val)))
	copy(data[4+4+4+1:], ent.key)
	copy(data[4+4+4+1+len(ent.key):], ent.val)
	binary.LittleEndian.PutUint32(data[0:4], crc32.ChecksumIEEE(data[4:]))
	return data
}

var ErrBadSum = errors.New("bad checksum")

func (ent *Entry) Decode(r io.Reader) error {
	var header [4 + 4 + 4 + 1]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	klen := int(binary.LittleEndian.Uint32(header[4:8]))
	vlen := int(binary.LittleEndian.Uint32(header[8:12]))
	op := EntryOp(header[4+4+4])

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

	ent.op = op
	ent.key = data[:klen]
	ent.val = data[klen:]
	return nil
}

// QzBQWVJJOUhU https://trialofcode.org/
