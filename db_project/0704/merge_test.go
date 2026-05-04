package db0704

import (
	"bytes"
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
			expected = append(expected, Entry{k[i], v[i], false})
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

// QzBQWVJJOUhU https://trialofcode.org/
