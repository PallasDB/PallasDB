package db0102

import (
	"encoding/binary"
	"io"
)

type Entry struct {
	key []byte
	val []byte
}

func (ent *Entry) Encode() []byte {
	data := make([]byte, 4+4+len(ent.key)+len(ent.val))
	binary.LittleEndian.PutUint32(data[0:4], uint32(len(ent.key)))
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.val)))
	copy(data[8:], ent.key)
	copy(data[8+len(ent.key):], ent.val)
	return data
}

func (ent *Entry) Decode(r io.Reader) error {
	keyLength := make([]byte, 4)
	if _, err := io.ReadFull(r, keyLength); err != nil {
		return err
	}

	valueLength := make([]byte, 4)
	if _, err := io.ReadFull(r, valueLength); err != nil {
		return err
	}

	// https://stackoverflow.com/questions/15848830/decoding-data-from-a-byte-slice-to-uint32
	entKey := make([]byte, binary.LittleEndian.Uint32(keyLength))
	if _, err := io.ReadFull(r, entKey); err != nil {
		return err
	}
	ent.key = entKey

	entValue := make([]byte, binary.LittleEndian.Uint32(valueLength))
	if _, err := io.ReadFull(r, entValue); err != nil {
		return err
	}
	ent.val = entValue

	return nil
}

// QzBQWVJJOUhU https://trialofcode.org/
