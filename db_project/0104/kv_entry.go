package db0104

import (
	"encoding/binary"
	"io"
)

type Entry struct {
	key     []byte
	val     []byte
	deleted bool
}

func (ent *Entry) Encode() []byte {
	data := make([]byte, 4+4+1+len(ent.key)+len(ent.val))
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(ent.key)))
	copy(data[9:], ent.key)
	if ent.deleted {
		data[8] = 1
	} else {
		binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.val)))
		copy(data[9+len(ent.key):], ent.val)
	}
	return data
}

func (ent *Entry) Decode(r io.Reader) error {
	var header [9]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	klen := int(binary.LittleEndian.Uint32(header[0:4]))
	vlen := int(binary.LittleEndian.Uint32(header[4:8]))
	deleted := header[8]

	data := make([]byte, klen+vlen)
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}

	ent.key = data[:klen]
	if deleted != 0 {
		ent.deleted = true
	} else {
		ent.deleted = false
		ent.val = data[klen:]
	}
	return nil
}

// QzBQWVJJOUhU https://trialofcode.org/
