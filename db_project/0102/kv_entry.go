package db0102

import (
	"io"
)

type Entry struct {
	key []byte
	val []byte
}

func (ent *Entry) Encode() []byte

func (ent *Entry) Decode(r io.Reader) error

// QzBQWVJJOUhU https://trialofcode.org/
