package db

import (
	"bytes"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKVBasic(t *testing.T) {
	kv := KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 1}}
	err := kv.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	updated, err := kv.Set([]byte("k1"), []byte("v1"))
	require.NoError(t, err)
	assert.True(t, updated)

	val, ok, err := kv.Get([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v1", string(val))

	_, ok, err = kv.Get([]byte("xxx"))
	require.NoError(t, err)
	assert.False(t, ok)

	updated, err = kv.Del([]byte("xxx"))
	require.NoError(t, err)
	assert.False(t, updated)

	updated, err = kv.Del([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, updated)

	_, ok, err = kv.Get([]byte("xxx"))
	require.NoError(t, err)
	assert.False(t, ok)

	updated, err = kv.Set([]byte("k2"), []byte("v2"))
	require.NoError(t, err)
	assert.True(t, updated)

	updated, err = kv.Set([]byte("k3"), []byte("v3"))
	require.NoError(t, err)
	assert.True(t, updated)

	// reopen
	require.NoError(t, kv.Close())
	err = kv.Open()
	require.Nil(t, err)

	_, ok, err = kv.Get([]byte("k1"))
	require.NoError(t, err)
	assert.False(t, ok)
	val, ok, err = kv.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v2", string(val))

	// compact
	err = kv.Compact()
	require.Nil(t, err)

	_, ok, err = kv.Get([]byte("k1"))
	require.NoError(t, err)
	assert.False(t, ok)
	val, ok, err = kv.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v2", string(val))

	updated, err = kv.Set([]byte("k2"), []byte("v2"))
	require.NoError(t, err)
	assert.False(t, updated)

	updated, err = kv.Del([]byte("k3"))
	require.NoError(t, err)
	assert.True(t, updated)
	_, ok, err = kv.Get([]byte("k3"))
	require.NoError(t, err)
	assert.False(t, ok)

	// reopen
	require.NoError(t, kv.Close())
	err = kv.Open()
	require.Nil(t, err)

	_, ok, err = kv.Get([]byte("k1"))
	require.NoError(t, err)
	assert.False(t, ok)
	val, ok, err = kv.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v2", string(val))
	_, ok, err = kv.Get([]byte("k3"))
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestKVReopen(t *testing.T) {
	for mode := 0; mode < 3; mode++ {
		t.Run(fmt.Sprintf("mode=%d", mode), func(t *testing.T) {
			path := t.TempDir()
			kv := KV{Options: KVOptions{Dirpath: path, LogShreshold: 1}}
			err := kv.Open()
			require.Nil(t, err)

			N := 20
			for i := 0; i < N; i++ {
				key := []byte(fmt.Sprintf("data%d", i))
				updated, err := kv.Set(key, key)
				require.Nil(t, err)
				require.True(t, updated)

				if mode == 0 || mode == 1 {
					err = kv.Compact()
					require.Nil(t, err)
				}
				if mode == 1 || mode == 2 {
					err = kv.Close()
					require.Nil(t, err)
					err = kv.Open()
					require.Nil(t, err)
				}

				for j := 0; j < i; j++ {
					key := []byte(fmt.Sprintf("data%d", j))
					val, ok, err := kv.Get(key)
					require.NoError(t, err)
					assert.True(t, ok)
					assert.Equal(t, string(key), string(val))
				}
			}

			err = kv.Close()
			require.Nil(t, err)
		})
	}
}

func TestKVUpdateMode(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()
	err := kv.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	updated, err := kv.SetEx([]byte("k1"), []byte("v1"), ModeUpdate)
	require.NoError(t, err)
	assert.False(t, updated)

	updated, err = kv.SetEx([]byte("k1"), []byte("v1"), ModeUpdate)
	require.NoError(t, err)
	assert.False(t, updated)

	updated, err = kv.SetEx([]byte("k1"), []byte("v1"), ModeInsert)
	require.NoError(t, err)
	assert.True(t, updated)

	updated, err = kv.SetEx([]byte("k1"), []byte("xx"), ModeInsert)
	require.NoError(t, err)
	assert.False(t, updated)

	updated, err = kv.SetEx([]byte("k1"), []byte("yy"), ModeUpdate)
	require.NoError(t, err)
	assert.True(t, updated)

	updated, err = kv.SetEx([]byte("k1"), []byte("zz"), ModeUpsert)
	require.NoError(t, err)
	assert.True(t, updated)

	updated, err = kv.SetEx([]byte("k2"), []byte("tt"), ModeUpsert)
	require.NoError(t, err)
	assert.True(t, updated)
}

func TestNewKVOptionsAndClose(t *testing.T) {
	kv, err := NewKV(t.TempDir(), WithLogThreshold(2), WithGrowthFactor(3), WithAutoCompact(true))
	require.NoError(t, err)
	assert.Equal(t, 2, kv.Options.LogThreshold)
	assert.Equal(t, 2, kv.Options.LogShreshold)
	assert.Equal(t, float32(3), kv.Options.GrowthFactor)
	assert.True(t, kv.Options.AutoCompact)
	assert.NoError(t, kv.Close())
	assert.NoError(t, kv.Close())

	_, err = NewKV(t.TempDir(), WithLogThreshold(0))
	assert.Error(t, err)
	_, err = NewKV(t.TempDir(), WithGrowthFactor(1))
	assert.Error(t, err)
}

func TestKVInvalidUpdateMode(t *testing.T) {
	kv := KV{Options: KVOptions{Dirpath: t.TempDir()}}
	require.NoError(t, kv.Open())
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	updated, err := kv.SetEx([]byte("k"), []byte("v"), ModeUnknown)
	assert.False(t, updated)
	assert.Error(t, err)
}

func TestKVRecovery(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()

	prepare := func() {
		require.NoError(t, os.RemoveAll(kv.Options.Dirpath))

		err := kv.Open()
		require.NoError(t, err)
		defer func() { assert.NoError(t, kv.Close()) }()

		updated, err := kv.Set([]byte("k1"), []byte("v1"))
		require.NoError(t, err)
		assert.True(t, updated)

		tx := kv.NewTX()
		updated, err = tx.Set([]byte("k3"), []byte("v3"))
		require.NoError(t, err)
		assert.True(t, updated)
		updated, err = tx.Set([]byte("k2"), []byte("v2"))
		require.NoError(t, err)
		assert.True(t, updated)
		err = tx.Commit()
		require.Nil(t, err)
	}

	prepare()
	// simulate truncated log
	fp, _ := os.OpenFile(kv.log.FileName, os.O_RDWR, 0o644)
	st, _ := fp.Stat()
	require.NoError(t, fp.Truncate(st.Size()-1))
	assert.NoError(t, fp.Close())
	// reopen
	err := kv.Open()
	assert.Nil(t, err)
	// test
	val, ok, err := kv.Get([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v1", string(val))
	_, ok, err = kv.Get([]byte("k2")) // bad
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = kv.Get([]byte("k3")) // bad
	require.NoError(t, err)
	assert.False(t, ok)
	assert.NoError(t, kv.Close())

	prepare()
	// simulate bad checksum
	fp, _ = os.OpenFile(kv.log.FileName, os.O_RDWR, 0o644)
	st, _ = fp.Stat()
	_, err = fp.WriteAt([]byte{0}, st.Size()-1)
	require.NoError(t, err)
	assert.NoError(t, fp.Close())
	// reopen
	err = kv.Open()
	assert.Nil(t, err)
	// test
	val, ok, err = kv.Get([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v1", string(val))
	_, ok, err = kv.Get([]byte("k2")) // bad
	require.NoError(t, err)
	assert.False(t, ok)
	_, ok, err = kv.Get([]byte("k3")) // bad
	require.NoError(t, err)
	assert.False(t, ok)
	assert.NoError(t, kv.Close())
}

func TestEntryEncode(t *testing.T) {
	ent := Entry{key: []byte("k1"), val: []byte("xxx")}
	data := []byte{0xe9, 0xec, 0x4d, 0x9e, 2, 0, 0, 0, 3, 0, 0, 0, 0, 'k', '1', 'x', 'x', 'x'}

	assert.Equal(t, data, ent.Encode())

	decoded := Entry{}
	err := decoded.Decode(bytes.NewBuffer(data))
	assert.Nil(t, err)
	assert.Equal(t, ent, decoded)

	ent = Entry{key: []byte("k1"), val: []byte{}, op: EntryDel}
	data = []byte{0x4c, 0xd0, 0xfe, 0xe5, 2, 0, 0, 0, 0, 0, 0, 0, 1, 'k', '1'}

	assert.Equal(t, data, ent.Encode())

	decoded = Entry{}
	err = decoded.Decode(bytes.NewBuffer(data))
	assert.Nil(t, err)
	assert.Equal(t, ent, decoded)
}

func TestKVSeek(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()
	err := kv.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	keys := []string{"c", "e", "g"}
	vals := []string{"3", "5", "7"}
	for i := range keys {
		_, _ = kv.Set([]byte(keys[i]), []byte(vals[i]))
	}
	// err = kv.Compact()
	// require.Nil(t, err)

	tx := kv.NewTX()
	iter, err := tx.Seek([]byte("a"))
	require.Nil(t, err)
	for i := range keys {
		assert.True(t, iter.Valid())
		assert.Equal(t, []byte(keys[i]), iter.Key())
		assert.Equal(t, []byte(vals[i]), iter.Val())
		err = iter.Next()
		require.Nil(t, err)
	}
	assert.False(t, iter.Valid())

	err = iter.Prev()
	require.Nil(t, err)
	for i := len(keys) - 1; i >= 0; i-- {
		assert.True(t, iter.Valid())
		assert.Equal(t, []byte(keys[i]), iter.Key())
		assert.Equal(t, []byte(vals[i]), iter.Val())
		err = iter.Prev()
		require.Nil(t, err)
	}
	assert.False(t, iter.Valid())

	iter, err = tx.Seek([]byte("f"))
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("g"), iter.Key())

	iter, err = tx.Seek([]byte("g"))
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("g"), iter.Key())

	iter, err = tx.Seek([]byte("h"))
	require.Nil(t, err)
	assert.False(t, iter.Valid())
	tx.Abort()
}

func TestKVSnapshot(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()
	err := kv.Open()
	require.Nil(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	updated, err := kv.Set([]byte("k1"), []byte("v1"))
	require.NoError(t, err)
	assert.True(t, updated)
	updated, err = kv.Set([]byte("k2"), []byte("v2"))
	require.NoError(t, err)
	assert.True(t, updated)

	tx1, tx2 := kv.NewTX(), kv.NewTX()
	val, ok, err := tx1.Get([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v1", string(val))
	val, ok, err = tx1.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v2", string(val))

	updated, err = tx2.Del([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, updated)
	updated, err = tx2.Set([]byte("k2"), []byte("xxx"))
	require.NoError(t, err)
	assert.True(t, updated)

	val, ok, err = tx1.Get([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v1", string(val))
	val, ok, err = tx1.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v2", string(val))

	_, ok, err = tx2.Get([]byte("k1"))
	require.NoError(t, err)
	assert.False(t, ok)
	val, ok, err = tx2.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "xxx", string(val))

	err = tx2.Commit()
	require.Nil(t, err)

	_, ok, err = kv.Get([]byte("k1"))
	require.NoError(t, err)
	assert.False(t, ok)
	val, ok, err = kv.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "xxx", string(val))

	val, ok, err = tx1.Get([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v1", string(val))
	val, ok, err = tx1.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "v2", string(val))
	tx1.Abort()
}

func TestTXConflict(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()
	err := kv.Open()
	require.Nil(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	updated, err := kv.Set([]byte("k1"), []byte("v1"))
	require.NoError(t, err)
	assert.True(t, updated)

	tx1, tx2 := kv.NewTX(), kv.NewTX()
	updated, err = tx1.Set([]byte("k1"), []byte("x"))
	require.NoError(t, err)
	assert.True(t, updated)
	updated, err = tx2.Set([]byte("k1"), []byte("y"))
	require.NoError(t, err)
	assert.True(t, updated)

	err = tx1.Commit()
	assert.Nil(t, err)
	err = tx2.Commit()
	assert.Equal(t, ErrTXConflict, err)

	updated, err = kv.Set([]byte("k1"), []byte("z"))
	require.NoError(t, err)
	assert.True(t, updated)
}

func TestKVAutoCompact(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()
	kv.Options.LogShreshold = 1
	kv.Options.AutoCompact = true
	err := kv.Open()
	require.Nil(t, err)

	N := 20
	for i := 0; i < N; i++ {
		data := fmt.Sprintf("k%d", i)
		updated, err := kv.Set([]byte(data), []byte(data))
		require.NoError(t, err)
		assert.True(t, updated)
	}
	assert.NoError(t, kv.Close())

	time.Sleep(time.Millisecond * 10)
	assert.True(t, kv.version > 10)
}

func TestKVClosing(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()
	kv.Options.AutoCompact = true
	err := kv.Open()
	require.Nil(t, err)

	updated, err := kv.Set([]byte("k1"), []byte("v1"))
	require.NoError(t, err)
	require.True(t, updated)

	t0 := time.Now()
	started := make(chan struct{}, 1)
	go func() {
		tx := kv.NewTX()
		defer tx.Abort()
		started <- struct{}{}
		time.Sleep(20 * time.Millisecond)
		val, ok, err := kv.Get([]byte("k1"))
		assert.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, "v1", string(val))
	}()
	<-started

	err = kv.Close()
	require.Nil(t, err)
	duration := time.Since(t0).Milliseconds()
	assert.True(t, duration >= 20)
}

func TestKVRange(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()
	err := kv.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	keys := []string{"b", "d", "f", "h"}
	for _, k := range keys {
		_, err = kv.Set([]byte(k), []byte(k+"v"))
		require.NoError(t, err)
	}

	tx := kv.NewTX()
	defer tx.Abort()

	// Forward range [b, f]
	iter, err := tx.Range([]byte("b"), []byte("f"), false)
	require.NoError(t, err)
	var got []string
	for iter.Valid() {
		got = append(got, string(iter.Key()))
		require.NoError(t, iter.Next())
	}
	assert.Equal(t, []string{"b", "d", "f"}, got)

	// Descending range [f, b]
	iter, err = tx.Range([]byte("f"), []byte("b"), true)
	require.NoError(t, err)
	got = nil
	for iter.Valid() {
		got = append(got, string(iter.Key()))
		require.NoError(t, iter.Next())
	}
	assert.Equal(t, []string{"f", "d", "b"}, got)

	// Empty range (start > stop in forward direction)
	iter, err = tx.Range([]byte("z"), []byte("a"), false)
	require.NoError(t, err)
	assert.False(t, iter.Valid())
}

func TestNoDeletedIter(t *testing.T) {
	arr := &SortedArray{}
	arr.Push([]byte("a"), []byte("1"), false)
	arr.Push([]byte("b"), nil, true) // deleted
	arr.Push([]byte("c"), []byte("3"), false)
	arr.Push([]byte("d"), nil, true) // deleted
	arr.Push([]byte("e"), []byte("5"), false)

	raw, err := arr.Iter()
	require.NoError(t, err)

	iter, err := filterDeleted(raw)
	require.NoError(t, err)

	// Forward traversal should skip deleted
	var keys []string
	for iter.Valid() {
		keys = append(keys, string(iter.Key()))
		require.NoError(t, iter.Next())
	}
	assert.Equal(t, []string{"a", "c", "e"}, keys)

	// Backward traversal should also skip deleted
	keys = nil
	for {
		require.NoError(t, iter.Prev())
		if !iter.Valid() {
			break
		}
		keys = append(keys, string(iter.Key()))
	}
	assert.Equal(t, []string{"e", "c", "a"}, keys)
}

func TestInnerTXApplyAndAbort(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = t.TempDir()
	err := kv.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	outer := kv.NewTX()

	// Set a key in outer
	_, err = outer.Set([]byte("k1"), []byte("v1"))
	require.NoError(t, err)

	// Create inner TX, make changes, then apply
	inner := outer.NewTX()
	_, err = inner.Set([]byte("k2"), []byte("v2"))
	require.NoError(t, err)
	err = inner.Commit()
	require.NoError(t, err)

	// outer should see k2
	val, ok, err := outer.Get([]byte("k2"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("v2"), val)

	// inner abort is a no-op
	inner2 := outer.NewTX()
	_, err = inner2.Set([]byte("k3"), []byte("v3"))
	require.NoError(t, err)
	inner2.Abort() // abort - changes should not appear in outer

	_, ok, err = outer.Get([]byte("k3"))
	require.NoError(t, err)
	assert.False(t, ok)

	outer.Abort()
}

func TestKVCompactMultipleLevels(t *testing.T) {
	kv := KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 1, GrowthFactor: 1.5}}
	err := kv.Open()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	// Create multiple SSTable levels via compaction
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key%d", i)
		_, err = kv.Set([]byte(key), []byte(key))
		require.NoError(t, err)
		err = kv.Compact()
		require.NoError(t, err)
	}

	// Verify all keys are accessible after compaction
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("key%d", i)
		val, ok, err := kv.Get([]byte(key))
		require.NoError(t, err)
		assert.True(t, ok, "key %s should exist", key)
		assert.Equal(t, []byte(key), val)
	}
}

// QzBQWVJJOUhU https://trialofcode.org/
