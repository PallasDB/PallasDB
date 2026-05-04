package db0601

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type SortedArray struct {
	keys [][]byte
	vals [][]byte
}

func (arr *SortedArray) Size() int {
	return len(arr.keys)
}
func (arr *SortedArray) Iter() (SortedKVIter, error) {
	return &KVIterator{arr.keys, arr.vals, 0}, nil
}

func TestSortedFile(t *testing.T) {
	sf := SortedFile{FileName: ".test_sorted_file"}
	defer os.Remove(sf.FileName)

	keys := [][]byte{[]byte("x"), []byte("y")}
	vals := [][]byte{[]byte("1"), []byte("234")}
	err := sf.CreateFromSorted(&SortedArray{keys, vals})
	require.Nil(t, err)
	defer sf.Close()

	expected := []byte{
		2, 0, 0, 0, 0, 0, 0, 0,
		24, 0, 0, 0, 0, 0, 0, 0,
		34, 0, 0, 0, 0, 0, 0, 0,
		1, 0, 0, 0, 1, 0, 0, 0, 'x', '1',
		1, 0, 0, 0, 3, 0, 0, 0, 'y', '2', '3', '4',
	}
	data, err := os.ReadFile(sf.FileName)
	require.Nil(t, err)
	assert.Equal(t, expected, data)
}

// QzBQWVJJOUhU https://trialofcode.org/
