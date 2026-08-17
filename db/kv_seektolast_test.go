package db

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// descendTX walks the whole keyspace backwards from the greatest live key.
func descendTX(tx *KVTX) ([]string, error) {
	iter, err := tx.SeekToLast()
	if err != nil {
		return nil, err
	}
	out := []string{}
	for iter.Valid() {
		out = append(out, string(iter.Key()))
		if err := iter.Prev(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// reverseOf returns a reversed copy of in.
func reverseOf(in []string) []string {
	out := slices.Clone(in)
	slices.Reverse(out)
	return out
}

func TestKVSeekToLast(t *testing.T) {
	t.Run("empty store", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir()}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		tx := kv.NewTX()
		defer tx.Abort()
		iter, err := tx.SeekToLast()
		require.NoError(t, err)
		assert.False(t, iter.Valid(), "empty store must yield an invalid iterator")
	})

	t.Run("single key", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir()}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		_, err := kv.Set([]byte("only"), []byte("val"))
		require.NoError(t, err)

		tx := kv.NewTX()
		defer tx.Abort()
		iter, err := tx.SeekToLast()
		require.NoError(t, err)
		require.True(t, iter.Valid())
		assert.Equal(t, "only", string(iter.Key()))
		assert.Equal(t, "val", string(iter.Val()))

		require.NoError(t, iter.Prev())
		assert.False(t, iter.Valid(), "stepping back off the first key must invalidate")
	})

	t.Run("single key in an sstable", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 1}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		_, err := kv.Set([]byte("only"), []byte("val"))
		require.NoError(t, err)
		require.NoError(t, kv.Compact())

		tx := kv.NewTX()
		defer tx.Abort()
		iter, err := tx.SeekToLast()
		require.NoError(t, err)
		require.True(t, iter.Valid())
		assert.Equal(t, "only", string(iter.Key()))
		assert.Equal(t, "val", string(iter.Val()))
	})

	t.Run("multiple levels with overlapping keys", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 1, GrowthFactor: 2}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		want := map[string]string{}
		set := func(key, val string) {
			_, err := kv.Set([]byte(key), []byte(val))
			require.NoError(t, err)
			want[key] = val
		}

		// Three generations, each flushed to its own level, with the later ones
		// overwriting keys the earlier ones own.
		for i := 0; i < 10; i++ {
			set(fmt.Sprintf("k%02d", i), fmt.Sprintf("gen0-%02d", i))
		}
		require.NoError(t, kv.Compact())
		for i := 0; i < 10; i += 3 {
			set(fmt.Sprintf("k%02d", i), fmt.Sprintf("gen1-%02d", i))
		}
		set("k10", "gen1-10")
		require.NoError(t, kv.Compact())
		for i := 1; i < 10; i += 4 {
			set(fmt.Sprintf("k%02d", i), fmt.Sprintf("gen2-%02d", i))
		}
		require.NoError(t, kv.Compact())

		tx := kv.NewTX()
		defer tx.Abort()

		iter, err := tx.SeekToLast()
		require.NoError(t, err)
		got := map[string]string{}
		keys := []string{}
		for iter.Valid() {
			key, val := string(iter.Key()), string(iter.Val())
			assert.NotContains(t, got, key, "duplicate key across levels")
			got[key] = val
			keys = append(keys, key)
			require.NoError(t, iter.Prev())
		}
		assert.Equal(t, want, got, "descending scan must resolve overlaps to the newest value")
		assert.Equal(t, reverseOf(keys), slices.Sorted(slices.Values(keys)), "descending scan is not ordered")
	})

	t.Run("skips tombstones at the end of the keyspace", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 1}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		for i := 0; i < 10; i++ {
			key := fmt.Sprintf("k%02d", i)
			_, err := kv.Set([]byte(key), []byte(key))
			require.NoError(t, err)
		}
		// Push them into an SSTable, then delete the tail so the tombstones sit
		// in a newer level than the values they shadow.
		require.NoError(t, kv.Compact())
		for _, key := range []string{"k09", "k08", "k07"} {
			deleted, err := kv.Del([]byte(key))
			require.NoError(t, err)
			require.True(t, deleted)
		}

		tx := kv.NewTX()
		defer tx.Abort()

		iter, err := tx.SeekToLast()
		require.NoError(t, err)
		require.True(t, iter.Valid())
		assert.Equal(t, "k06", string(iter.Key()), "iterator started on a tombstone")

		desc, err := descendTX(tx)
		require.NoError(t, err)
		assert.Equal(t, []string{"k06", "k05", "k04", "k03", "k02", "k01", "k00"}, desc)
	})

	t.Run("every key deleted", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 1}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		for i := 0; i < 4; i++ {
			key := fmt.Sprintf("k%02d", i)
			_, err := kv.Set([]byte(key), []byte(key))
			require.NoError(t, err)
		}
		require.NoError(t, kv.Compact())
		for i := 0; i < 4; i++ {
			_, err := kv.Del([]byte(fmt.Sprintf("k%02d", i)))
			require.NoError(t, err)
		}

		tx := kv.NewTX()
		defer tx.Abort()
		iter, err := tx.SeekToLast()
		require.NoError(t, err)
		assert.False(t, iter.Valid(), "an all-tombstone store must yield an invalid iterator")
	})

	t.Run("mirrors the ascending scan", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 4, GrowthFactor: 2}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		for i := 0; i < 40; i++ {
			key := fmt.Sprintf("key%03d", i)
			_, err := kv.Set([]byte(key), []byte(key))
			require.NoError(t, err)
			if i%7 == 0 {
				require.NoError(t, kv.Compact())
			}
		}
		for i := 0; i < 40; i += 5 {
			_, err := kv.Del([]byte(fmt.Sprintf("key%03d", i)))
			require.NoError(t, err)
		}
		require.NoError(t, kv.Compact())

		tx := kv.NewTX()
		defer tx.Abort()

		asc, err := scanTX(tx)
		require.NoError(t, err)
		require.NotEmpty(t, asc)
		desc, err := descendTX(tx)
		require.NoError(t, err)
		assert.Equal(t, reverseOf(asc), desc, "descending scan is not the reverse of the ascending one")
	})

	t.Run("rejected after the transaction ends", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir()}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		_, err := kv.Set([]byte("k"), []byte("v"))
		require.NoError(t, err)

		tx := kv.NewTX()
		tx.Abort()
		_, err = tx.SeekToLast()
		assert.ErrorIs(t, err, ErrTXDone)

		tx = kv.NewTX()
		require.NoError(t, tx.Commit())
		_, err = tx.SeekToLast()
		assert.ErrorIs(t, err, ErrTXDone)

		require.NoError(t, kv.Close())
		tx = kv.NewTX()
		defer tx.Abort()
		_, err = tx.SeekToLast()
		assert.ErrorIs(t, err, ErrKVClosed)
		require.NoError(t, kv.Open()) // the cleanup Close expects an open store
	})

	t.Run("holds its snapshot across compaction", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 2, GrowthFactor: 2}}
		require.NoError(t, kv.Open())
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		for i := 0; i < 20; i++ {
			key := fmt.Sprintf("key%03d", i)
			_, err := kv.Set([]byte(key), []byte(key))
			require.NoError(t, err)
			if i%4 == 0 {
				require.NoError(t, kv.Compact())
			}
		}
		require.NoError(t, kv.Compact())

		tx := kv.NewTX()
		defer tx.Abort()
		want, err := descendTX(tx)
		require.NoError(t, err)
		require.NotEmpty(t, want)

		// Start the descending walk, then retire every table it captured
		// underneath it: new writes, flushes and merges.
		iter, err := tx.SeekToLast()
		require.NoError(t, err)
		require.True(t, iter.Valid())
		got := []string{string(iter.Key())}
		require.NoError(t, iter.Prev())

		for i := 20; i < 40; i++ {
			key := fmt.Sprintf("key%03d", i)
			_, err := kv.Set([]byte(key), []byte(key))
			require.NoError(t, err)
			require.NoError(t, kv.Compact())
		}
		for i := 0; i < 20; i += 2 {
			_, err := kv.Del([]byte(fmt.Sprintf("key%03d", i)))
			require.NoError(t, err)
		}
		require.NoError(t, kv.Compact())

		// The retired tables are still readable through the snapshot.
		for iter.Valid() {
			got = append(got, string(iter.Key()))
			require.NoError(t, iter.Prev())
		}
		assert.Equal(t, want, got, "compaction changed what the snapshot could see")
	})
}
