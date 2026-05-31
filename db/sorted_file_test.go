package db

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSortedFile(t *testing.T) {
	sf := SortedFile{FileName: ".test_sorted_file"}
	t.Cleanup(func() { _ = os.Remove(sf.FileName) })

	keys := [][]byte{[]byte("x"), []byte("x2"), []byte("y")}
	vals := [][]byte{[]byte("1"), []byte(""), []byte("234")}
	deleted := []bool{false, true, false}
	err := sf.CreateFromSorted(&SortedArray{keys, vals, deleted})
	require.Nil(t, err)
	t.Cleanup(func() { assert.NoError(t, sf.Close()) })
	assert.Equal(t, 3, sf.EstimatedSize())

	expected := []byte{
		3, 0, 0, 0, 0, 0, 0, 0,
		32, 0, 0, 0, 0, 0, 0, 0,
		43, 0, 0, 0, 0, 0, 0, 0,
		54, 0, 0, 0, 0, 0, 0, 0,
		1, 0, 0, 0, 1, 0, 0, 0, 0, 'x', '1',
		2, 0, 0, 0, 0, 0, 0, 0, 1, 'x', '2',
		1, 0, 0, 0, 3, 0, 0, 0, 0, 'y', '2', '3', '4',
	}
	data, err := os.ReadFile(sf.FileName)
	require.Nil(t, err)
	require.GreaterOrEqual(t, len(data), len(expected)+sortedFileBloomFooterSize)
	assert.Equal(t, expected, data[:len(expected)])
	footer := data[len(data)-sortedFileBloomFooterSize:]
	assert.Equal(t, sortedFileBloomMagic[:], footer[8:])
	assert.Greater(t, binary.LittleEndian.Uint64(footer[:8]), uint64(0))
	assert.NotNil(t, sf.bloom)

	i := 0
	iter, err := sf.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		assert.Equal(t, keys[i], iter.Key())
		assert.Equal(t, vals[i], iter.Val())
		i++
	}
	require.Nil(t, err)

	iter, err = sf.Seek([]byte("xx"))
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("y"), iter.Key())
}

func TestSortedFileEmpty(t *testing.T) {
	sf := SortedFile{FileName: ".test_sorted_file_empty"}
	t.Cleanup(func() { _ = os.Remove(sf.FileName) })

	// Create empty sorted file
	err := sf.CreateFromSorted(&SortedArray{})
	require.Nil(t, err)
	t.Cleanup(func() { assert.NoError(t, sf.Close()) })
	assert.Equal(t, 0, sf.EstimatedSize())

	iter, err := sf.Iter()
	require.Nil(t, err)
	assert.False(t, iter.Valid())

	iter, err = sf.Seek([]byte("anything"))
	require.Nil(t, err)
	assert.False(t, iter.Valid())
}

func TestSortedFilePrev(t *testing.T) {
	sf := SortedFile{FileName: ".test_sorted_file_prev"}
	t.Cleanup(func() { _ = os.Remove(sf.FileName) })

	keys := [][]byte{[]byte("a"), []byte("b"), []byte("c")}
	vals := [][]byte{[]byte("1"), []byte("2"), []byte("3")}
	deleted := []bool{false, false, false}
	err := sf.CreateFromSorted(&SortedArray{keys, vals, deleted})
	require.Nil(t, err)
	t.Cleanup(func() { assert.NoError(t, sf.Close()) })

	// Iterate forward to end
	iter, err := sf.Iter()
	require.Nil(t, err)
	for ; iter.Valid(); err = iter.Next() {
		require.Nil(t, err)
	}
	require.Nil(t, err)
	assert.False(t, iter.Valid())

	// Iterate backward
	i := len(keys) - 1
	for err = iter.Prev(); err == nil && iter.Valid(); err = iter.Prev() {
		assert.Equal(t, keys[i], iter.Key())
		assert.Equal(t, vals[i], iter.Val())
		i--
	}
	require.Nil(t, err)
	assert.False(t, iter.Valid())
	assert.Equal(t, -1, i)
}

func TestSortedFileSeekEdgeCases(t *testing.T) {
	sf := SortedFile{FileName: ".test_sorted_file_seek"}
	t.Cleanup(func() { _ = os.Remove(sf.FileName) })

	keys := [][]byte{[]byte("b"), []byte("d"), []byte("f")}
	vals := [][]byte{[]byte("2"), []byte("4"), []byte("6")}
	deleted := []bool{false, false, false}
	err := sf.CreateFromSorted(&SortedArray{keys, vals, deleted})
	require.Nil(t, err)
	t.Cleanup(func() { assert.NoError(t, sf.Close()) })

	// Seek before first key
	iter, err := sf.Seek([]byte("a"))
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("b"), iter.Key())

	// Seek to exact key
	iter, err = sf.Seek([]byte("d"))
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("d"), iter.Key())

	// Seek past last key
	iter, err = sf.Seek([]byte("z"))
	require.Nil(t, err)
	assert.False(t, iter.Valid())
}

func TestSortedFileDeletedFlag(t *testing.T) {
	sf := SortedFile{FileName: ".test_sorted_file_del"}
	t.Cleanup(func() { _ = os.Remove(sf.FileName) })

	arr := &SortedArray{}
	arr.Push([]byte("a"), []byte("1"), false)
	arr.Push([]byte("b"), nil, true) // deleted
	err := sf.CreateFromSorted(arr)
	require.Nil(t, err)
	t.Cleanup(func() { assert.NoError(t, sf.Close()) })

	iter, err := sf.Iter()
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.False(t, iter.Deleted())
	assert.Equal(t, []byte("a"), iter.Key())

	require.Nil(t, iter.Next())
	assert.True(t, iter.Valid())
	assert.True(t, iter.Deleted())
	assert.Equal(t, []byte("b"), iter.Key())
}

func TestSortedFileReopen(t *testing.T) {
	fname := ".test_sorted_file_reopen"
	sf := SortedFile{FileName: fname}
	t.Cleanup(func() { _ = os.Remove(fname) })

	arr := &SortedArray{}
	arr.Push([]byte("key1"), []byte("val1"), false)
	arr.Push([]byte("key2"), []byte("val2"), false)

	err := sf.CreateFromSorted(arr)
	require.Nil(t, err)
	require.NotNil(t, sf.bloom)
	require.Nil(t, sf.Close())

	// Reopen and verify
	sf2 := SortedFile{FileName: fname}
	err = sf2.Open()
	require.Nil(t, err)
	defer func() { assert.NoError(t, sf2.Close()) }()

	assert.Equal(t, 2, sf2.EstimatedSize())
	require.NotNil(t, sf2.bloom)
	assert.True(t, sf2.bloom.Test([]byte("key1")))
	assert.True(t, sf2.bloom.Test([]byte("key2")))

	iter, err := sf2.Iter()
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("key1"), iter.Key())
	assert.Equal(t, []byte("val1"), iter.Val())
}

func TestSortedFileBloomSkipsImpossiblePointLookup(t *testing.T) {
	sf := SortedFile{FileName: ".test_sorted_file_bloom"}
	t.Cleanup(func() { _ = os.Remove(sf.FileName) })

	arr := &SortedArray{}
	for i := byte('a'); i <= 'z'; i++ {
		k := []byte{i}
		arr.Push(k, k, false)
	}
	err := sf.CreateFromSorted(arr)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sf.Close()) })
	require.NotNil(t, sf.bloom)

	var missing []byte
	for i := 0; i < 10000; i++ {
		candidate := []byte("missing")
		candidate = binary.LittleEndian.AppendUint64(candidate, uint64(i))
		if !sf.mayContainKey(candidate) {
			missing = candidate
			break
		}
	}
	require.NotNil(t, missing)

	offsets := bytes.Repeat([]byte{0}, 8*sf.nkeys)
	_, err = sf.fp.WriteAt(offsets, 8)
	require.NoError(t, err)

	val, found, deleted, err := sf.getExact(missing)
	require.NoError(t, err)
	assert.False(t, found)
	assert.False(t, deleted)
	assert.Nil(t, val)
}

// QzBQWVJJOUhU https://trialofcode.org/
