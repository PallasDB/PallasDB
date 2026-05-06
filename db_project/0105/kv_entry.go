package db0105

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
)

type Entry struct {
	key     []byte
	val     []byte
	deleted bool
}

func (ent *Entry) Encode() []byte {
	data := make([]byte, 4+4+4+1+len(ent.key)+len(ent.val))
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.key)))
	copy(data[13:], ent.key)
	if ent.deleted {
		data[12] = 1
	} else {
		binary.LittleEndian.PutUint32(data[8:12], uint32(len(ent.val)))
		copy(data[13+len(ent.key):], ent.val)
	}
	binary.LittleEndian.PutUint32(data[0:4], crc32.ChecksumIEEE(data[4:]))
	return data
}

var ErrBadSum = errors.New("bad checksum")

func (ent *Entry) Decode(r io.Reader) error {
	// read the whole header
	var header [13]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	
	storedSum := binary.LittleEndian.Uint32(header[0:4])
	keyLength := int(binary.LittleEndian.Uint32(header[4:8]))
	valueLength := int(binary.LittleEndian.Uint32(header[8:12]))
	deleted := header[12]

	// save both the key and val in a single variable
	keyval := make([]byte, keyLength+valueLength)
	if _, err := io.ReadFull(r, keyval); err != nil {
		return err
	}

	h := crc32.NewIEEE()
	h.Write(header[4:])
	h.Write(keyval)
	if h.Sum32() != storedSum {
		return ErrBadSum
	}

	ent.key = keyval[:keyLength]
	if deleted != 0 {
		ent.deleted = true
	} else {
		ent.deleted = false
		ent.val = keyval[keyLength:]
	}
	return nil
}

// QzBQWVJJOUhU https://trialofcode.org/
