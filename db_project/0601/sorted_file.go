package db0601

import (
	"os"
)

type SortedKV interface {
	Size() int
	Iter() (SortedKVIter, error)
}

type SortedKVIter interface {
	Valid() bool
	Key() []byte
	Val() []byte
	Next() error
	Prev() error
}

type SortedFile struct {
	FileName string
	fp       *os.File
}

func (file *SortedFile) Close() error {
	return file.fp.Close()
}

func (file *SortedFile) CreateFromSorted(kv SortedKV) (err error)

// QzBQWVJJOUhU https://trialofcode.org/
