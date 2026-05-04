package db0701

import (
	"bytes"
	"slices"
)

type SortedArray struct {
	keys    [][]byte
	vals    [][]byte
	deleted []bool
}

func (arr *SortedArray) Size() int { return len(arr.keys) }

func (arr *SortedArray) EstimatedSize() int {
	return len(arr.keys)
}

func (arr *SortedArray) Iter() (SortedKVIter, error) {
	return &SortedArrayIter{arr.keys, arr.vals, arr.deleted, 0}, nil
}

func (arr *SortedArray) Seek(key []byte) (SortedKVIter, error) {
	pos, _ := slices.BinarySearchFunc(arr.keys, key, bytes.Compare)
	return &SortedArrayIter{keys: arr.keys, vals: arr.vals, deleted: arr.deleted, pos: pos}, nil
}

type SortedArrayIter struct {
	keys    [][]byte
	vals    [][]byte
	deleted []bool
	pos     int
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

func (iter *SortedArrayIter) Deleted() bool {
	check(iter.Valid())
	return iter.deleted[iter.pos]
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
	arr.keys, arr.vals, arr.deleted = arr.keys[:0], arr.vals[:0], arr.deleted[:0]
}

func (arr *SortedArray) Push(key []byte, val []byte, deleted bool) {
	arr.keys = append(arr.keys, key)
	arr.vals = append(arr.vals, val)
	arr.deleted = append(arr.deleted, deleted)
}

func (arr *SortedArray) Pop() {
	n := arr.Size()
	arr.keys, arr.vals, arr.deleted = arr.keys[:n-1], arr.vals[:n-1], arr.deleted[:n-1]
}

func (arr *SortedArray) Key(i int) []byte {
	return arr.keys[i]
}

func (arr *SortedArray) Set(key []byte, val []byte) (updated bool, err error) {
	idx, ok := slices.BinarySearchFunc(arr.keys, key, bytes.Compare)
	updated = !ok || arr.deleted[idx] || !bytes.Equal(val, arr.vals[idx])
	if updated {
		if ok {
			arr.vals[idx] = val
			arr.deleted[idx] = false
		} else {
			arr.keys = slices.Insert(arr.keys, idx, key)
			arr.vals = slices.Insert(arr.vals, idx, val)
			arr.deleted = slices.Insert(arr.deleted, idx, false)
		}
	}
	return updated, nil
}

func (arr *SortedArray) Del(key []byte) (deleted bool, err error) {
	idx, ok := slices.BinarySearchFunc(arr.keys, key, bytes.Compare)
	exist := ok && !arr.deleted[idx]
	if exist {
		arr.vals[idx] = nil
		arr.deleted[idx] = true
		return true, nil
	} else {
		arr.keys = slices.Insert(arr.keys, idx, key)
		arr.vals = slices.Insert(arr.vals, idx, nil)
		arr.deleted = slices.Insert(arr.deleted, idx, true)
		return false, nil
	}
}

// QzBQWVJJOUhU https://trialofcode.org/
