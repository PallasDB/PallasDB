package db0604

type MergedSortedKV []SortedKV

func (m MergedSortedKV) Iter() (iter SortedKVIter, err error)

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

func (iter *MergedSortedKVIter) Next() error

func (iter *MergedSortedKVIter) Prev() error

// QzBQWVJJOUhU https://trialofcode.org/
