package db0603

import (
	"bytes"
	"slices"
)

type SortedArray struct {
	keys [][]byte
	vals [][]byte
}

func (arr *SortedArray) Size() int {
	return len(arr.keys)
}

func (arr *SortedArray) Iter() (SortedKVIter, error) {
	return &SortedArrayIter{arr.keys, arr.vals, 0}, nil
}

func (arr *SortedArray) Seek(key []byte) (SortedKVIter, error) {
	pos, _ := slices.BinarySearchFunc(arr.keys, key, bytes.Compare)
	return &SortedArrayIter{keys: arr.keys, vals: arr.vals, pos: pos}, nil
}

type SortedArrayIter struct {
	keys [][]byte
	vals [][]byte
	pos  int
}

func (iter *SortedArrayIter) Valid() bool {
	return 0 <= iter.pos && iter.pos < len(iter.keys)
}

func (iter *SortedArrayIter) Key() []byte {
	check(iter.Valid())
	return iter.keys[iter.pos]
}

func (iter *SortedArrayIter) Val() []byte {
	check(iter.Valid())
	return iter.vals[iter.pos]
}

func (iter *SortedArrayIter) Next() error {
	if iter.pos < len(iter.keys) {
		iter.pos++
	}
	return nil
}

func (iter *SortedArrayIter) Prev() error {
	if iter.pos >= 0 {
		iter.pos--
	}
	return nil
}

func (arr *SortedArray) Clear() {
	arr.keys, arr.vals = arr.keys[:0], arr.vals[:0]
}

func (arr *SortedArray) Push(key []byte, val []byte) {
	arr.keys = append(arr.keys, key)
	arr.vals = append(arr.vals, val)
}

func (arr *SortedArray) Pop() {
	n := arr.Size()
	arr.keys, arr.vals = arr.keys[:n-1], arr.vals[:n-1]
}

func (arr *SortedArray) Key(i int) []byte {
	return arr.keys[i]
}

func (arr *SortedArray) Get(key []byte) (val []byte, ok bool, err error) {
	if idx, ok := slices.BinarySearchFunc(arr.keys, key, bytes.Compare); ok {
		return arr.vals[idx], true, nil
	}
	return nil, false, nil
}

func (arr *SortedArray) Set(key []byte, val []byte) (updated bool, err error)

func (arr *SortedArray) Del(key []byte) (deleted bool, err error)

// QzBQWVJJOUhU https://trialofcode.org/
