package db0105

import (
	"errors"
	"io"
)

type Entry struct {
	key     []byte
	val     []byte
	deleted bool
}

func (ent *Entry) Encode() []byte

var ErrBadSum = errors.New("bad checksum")

func (ent *Entry) Decode(r io.Reader) error

// QzBQWVJJOUhU https://trialofcode.org/
