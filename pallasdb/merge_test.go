package pallasdb

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func slist2blist(list []string) (out [][]byte) {
	for _, v := range list {
		out = append(out, ([]byte)(v))
	}
	return out
}

func testMerge(t *testing.T, alist ...[]string) {
	t.Helper()
	dup := map[string]bool{}
	expected := []Entry{}

	kl := [][][]byte{}
	vl := [][][]byte{}
	for i, a := range alist {
		k := slist2blist(a)
		kl = append(kl, k)
		v := [][]byte{}
		for range a {
			v = append(v, []byte{'A' + byte(i)})
		}
		vl = append(vl, v)

		for i, key := range a {
			if dup[key] {
				continue
			}
			dup[key] = true
			expected = append(expected, Entry{k[i], v[i], EntryAdd})
		}
	}

	slices.SortStableFunc(expected, func(a, b Entry) int {
		return bytes.Compare(a.key, b.key)
	})

	seqs := []SortedKV{}
	for i, k := range kl {
		seq := &SortedArray{keys: k, vals: vl[i]}
		seqs = append(seqs, seq)
	}
	m := MergedSortedKV(seqs)

	i := 0
	iter, err := m.Iter()
	for ; err == nil && iter.Valid(); err = iter.Next() {
		assert.Equal(t, expected[i].key, iter.Key())
		assert.Equal(t, expected[i].val, iter.Val())
		i++
	}
	require.Nil(t, err)
	assert.False(t, iter.Valid())
	assert.Equal(t, len(expected), i)

	for err = iter.Prev(); err == nil && iter.Valid(); err = iter.Prev() {
		i--
		assert.Equal(t, expected[i].key, iter.Key())
		assert.Equal(t, expected[i].val, iter.Val())
	}
	require.Nil(t, err)
	assert.False(t, iter.Valid())
	assert.Equal(t, 0, i)

	for ; err == nil && iter.Valid(); err = iter.Next() {
		assert.Equal(t, expected[i].key, iter.Key())
		assert.Equal(t, expected[i].val, iter.Val())

		err = iter.Prev()
		require.Nil(t, err)
		i--
		assert.Equal(t, expected[i].key, iter.Key())
		assert.Equal(t, expected[i].val, iter.Val())

		err = iter.Next()
		require.Nil(t, err)
		i += 2
	}
}

func TestMerge(t *testing.T) {
	a := []string{}
	b := []string{}
	testMerge(t, a, b)
	a = []string{"x", "z"}
	b = []string{}
	testMerge(t, a, b)
	a = []string{}
	b = []string{"x", "z"}
	testMerge(t, a, b)
	a = []string{"x", "z"}
	b = []string{"x", "z"}
	testMerge(t, a, b)
	a = []string{"x", "z"}
	b = []string{"w", "y"}
	testMerge(t, a, b)
	a, b = b, a
	testMerge(t, a, b)
}

func TestSortedArrayOperations(t *testing.T) {
	arr := &SortedArray{}

	// Empty array
	assert.Equal(t, 0, arr.Size())
	assert.Equal(t, 0, arr.EstimatedSize())

	// Push and verify
	arr.Push([]byte("b"), []byte("vb"), false)
	arr.Push([]byte("a"), []byte("va"), false)
	arr.Push([]byte("c"), []byte("vc"), true)
	assert.Equal(t, 3, arr.Size())

	// Key(i) accessor
	assert.Equal(t, []byte("b"), arr.Key(0))
	assert.Equal(t, []byte("a"), arr.Key(1))
	assert.Equal(t, []byte("c"), arr.Key(2))

	// Pop removes last element
	arr.Pop()
	assert.Equal(t, 2, arr.Size())
	assert.Equal(t, []byte("a"), arr.Key(1))

	// Clear empties the array
	arr.Clear()
	assert.Equal(t, 0, arr.Size())
}

func TestSortedArraySetDel(t *testing.T) {
	arr := &SortedArray{}

	// Set inserts
	updated, err := arr.Set([]byte("k1"), []byte("v1"))
	require.NoError(t, err)
	assert.True(t, updated)

	// Set same key same value - no update
	updated, err = arr.Set([]byte("k1"), []byte("v1"))
	require.NoError(t, err)
	assert.False(t, updated)

	// Set same key different value - update
	updated, err = arr.Set([]byte("k1"), []byte("v2"))
	require.NoError(t, err)
	assert.True(t, updated)

	// Del existing key
	deleted, err := arr.Del([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, deleted)

	// Del a key that was deleted (tombstone case - exists as deleted)
	deleted, err = arr.Del([]byte("k1"))
	require.NoError(t, err)
	assert.False(t, deleted) // already marked deleted

	// Del non-existing key (inserts tombstone)
	deleted, err = arr.Del([]byte("nonexistent"))
	require.NoError(t, err)
	assert.False(t, deleted)

	// Set after delete should be treated as updated (re-insert)
	updated, err = arr.Set([]byte("k1"), []byte("v3"))
	require.NoError(t, err)
	assert.True(t, updated)
}

func TestSortedArrayIter(t *testing.T) {
	arr := &SortedArray{}
	arr.Push([]byte("a"), []byte("1"), false)
	arr.Push([]byte("b"), []byte("2"), true) // deleted
	arr.Push([]byte("c"), []byte("3"), false)

	iter, err := arr.Iter()
	require.NoError(t, err)

	// First element
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("a"), iter.Key())
	assert.Equal(t, []byte("1"), iter.Val())
	assert.False(t, iter.Deleted())

	// Next - deleted element
	require.NoError(t, iter.Next())
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("b"), iter.Key())
	assert.True(t, iter.Deleted())

	// Next - last element
	require.NoError(t, iter.Next())
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("c"), iter.Key())

	// Next - end of array
	require.NoError(t, iter.Next())
	assert.False(t, iter.Valid())

	// Prev from end
	require.NoError(t, iter.Prev())
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("c"), iter.Key())

	// Prev to start
	require.NoError(t, iter.Prev())
	require.NoError(t, iter.Prev())
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("a"), iter.Key())

	// Prev past start
	require.NoError(t, iter.Prev())
	assert.False(t, iter.Valid())
}

func TestSortedArraySeek(t *testing.T) {
	arr := &SortedArray{}
	arr.Push([]byte("a"), []byte("1"), false)
	arr.Push([]byte("c"), []byte("3"), false)
	arr.Push([]byte("e"), []byte("5"), false)

	// Seek to existing key
	iter, err := arr.Seek([]byte("c"))
	require.NoError(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("c"), iter.Key())

	// Seek to key between existing keys
	iter, err = arr.Seek([]byte("b"))
	require.NoError(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("c"), iter.Key())

	// Seek past last key
	iter, err = arr.Seek([]byte("z"))
	require.NoError(t, err)
	assert.False(t, iter.Valid())

	// Seek before first key
	iter, err = arr.Seek([]byte(""))
	require.NoError(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("a"), iter.Key())
}

func TestMultiClosers(t *testing.T) {
	// Close with no items - should not error
	mc := MultiClosers{}
	err := mc.Close()
	assert.NoError(t, err)
	assert.Nil(t, ([]io.Closer)(mc))

	// Close with multiple items - returns last error
	errClose := errors.New("close error")
	mc = MultiClosers{
		io.NopCloser(nil),
		errorCloser{errClose},
		io.NopCloser(nil),
	}
	err = mc.Close()
	assert.ErrorIs(t, err, errClose)
	// After close, slice should be nil
	assert.Nil(t, ([]io.Closer)(mc))
}

// errorCloser is an io.Closer that always returns an error.
type errorCloser struct{ err error }

func (e errorCloser) Close() error { return e.err }

// QzBQWVJJOUhU https://trialofcode.org/
