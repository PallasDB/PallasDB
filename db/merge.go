package db

import "bytes"

type sortedKVExactGetter interface {
	getExact(key []byte) (val []byte, found bool, deleted bool, err error)
}

func getExactFromSortedKV(kv SortedKV, key []byte) (val []byte, found bool, deleted bool, err error) {
	if getter, ok := kv.(sortedKVExactGetter); ok {
		return getter.getExact(key)
	}
	iter, err := kv.Seek(key)
	if err != nil || !iter.Valid() || !bytes.Equal(iter.Key(), key) {
		return nil, false, false, err
	}
	return iter.Val(), true, iter.Deleted(), nil
}

type MergedSortedKV []SortedKV

func (m MergedSortedKV) EstimatedSize() (total int) {
	for _, sub := range m {
		total += sub.EstimatedSize()
	}
	return total
}

func (m MergedSortedKV) Iter() (iter SortedKVIter, err error) {
	levels := make([]SortedKVIter, len(m))
	for i, sub := range m {
		if levels[i], err = sub.Iter(); err != nil {
			return nil, err
		}
	}
	return &MergedSortedKVIter{levels, levelsLowest(levels)}, nil
}

func (m MergedSortedKV) Seek(key []byte) (iter SortedKVIter, err error) {
	levels := make([]SortedKVIter, len(m))
	for i, sub := range m {
		if levels[i], err = sub.Seek(key); err != nil {
			return nil, err
		}
	}
	return &MergedSortedKVIter{levels, levelsLowest(levels)}, nil
}

func (m MergedSortedKV) SeekToLast() (iter SortedKVIter, err error) {
	levels := make([]SortedKVIter, len(m))
	for i, sub := range m {
		if levels[i], err = sub.SeekToLast(); err != nil {
			return nil, err
		}
	}
	// Every level sits on its own greatest key; the winner is the greatest of
	// those, which is exactly the state Prev expects.
	return &MergedSortedKVIter{levels, levelsHighest(levels)}, nil
}

func (m MergedSortedKV) getExact(key []byte) (val []byte, found bool, deleted bool, err error) {
	for _, sub := range m {
		val, found, deleted, err = getExactFromSortedKV(sub, key)
		if err != nil || found {
			return val, found, deleted, err
		}
	}
	return nil, false, false, nil
}

type MergedSortedKVIter struct {
	levels []SortedKVIter
	which  int
}

func (iter *MergedSortedKVIter) Valid() bool {
	return iter.which >= 0
}
func (iter *MergedSortedKVIter) Key() []byte {
	return iter.levels[iter.which].Key()
}
func (iter *MergedSortedKVIter) Val() []byte {
	return iter.levels[iter.which].Val()
}
func (iter *MergedSortedKVIter) Deleted() bool {
	return iter.levels[iter.which].Deleted()
}

func (iter *MergedSortedKVIter) Next() error {
	cur := ([]byte)(nil)
	if iter.Valid() {
		cur = iter.Key()
	}
	for _, sub := range iter.levels {
		if !sub.Valid() || bytes.Compare(cur, sub.Key()) >= 0 {
			if err := sub.Next(); err != nil {
				return err
			}
		}
	}
	iter.which = levelsLowest(iter.levels)
	return nil
}

func levelsLowest(levels []SortedKVIter) int {
	win := -1
	winKey := []byte(nil)
	for i, sub := range levels {
		if sub.Valid() && (win < 0 || bytes.Compare(winKey, sub.Key()) > 0) {
			win, winKey = i, sub.Key()
		}
	}
	return win
}

func (iter *MergedSortedKVIter) Prev() error {
	cur := ([]byte)(nil)
	if iter.Valid() {
		cur = iter.Key()
	}
	for _, sub := range iter.levels {
		if !sub.Valid() || bytes.Compare(cur, sub.Key()) <= 0 {
			if err := sub.Prev(); err != nil {
				return err
			}
		}
	}
	iter.which = levelsHighest(iter.levels)
	return nil
}

func levelsHighest(levels []SortedKVIter) int {
	win := -1
	winKey := []byte(nil)
	for i, sub := range levels {
		if sub.Valid() && (win < 0 || bytes.Compare(winKey, sub.Key()) < 0) {
			win, winKey = i, sub.Key()
		}
	}
	return win
}
