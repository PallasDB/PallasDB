package db0904

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
	kv := KV{Options: KVOptions{Dirpath: "test_db", LogShreshold: 1}}
	defer os.RemoveAll(kv.Options.Dirpath)

	os.RemoveAll(kv.Options.Dirpath)
	err := kv.Open()
	assert.Nil(t, err)
	defer kv.Close()

	updated, err := kv.Set([]byte("k1"), []byte("v1"))
	assert.True(t, updated && err == nil)

	val, ok, err := kv.Get([]byte("k1"))
	assert.True(t, string(val) == "v1" && ok && err == nil)

	_, ok, err = kv.Get([]byte("xxx"))
	assert.True(t, !ok && err == nil)

	updated, err = kv.Del([]byte("xxx"))
	assert.True(t, !updated && err == nil)

	updated, err = kv.Del([]byte("k1"))
	assert.True(t, updated && err == nil)

	_, ok, err = kv.Get([]byte("xxx"))
	assert.True(t, !ok && err == nil)

	updated, err = kv.Set([]byte("k2"), []byte("v2"))
	assert.True(t, updated && err == nil)

	updated, err = kv.Set([]byte("k3"), []byte("v3"))
	assert.True(t, updated && err == nil)

	// reopen
	kv.Close()
	err = kv.Open()
	require.Nil(t, err)

	_, ok, err = kv.Get([]byte("k1"))
	assert.True(t, !ok && err == nil)
	val, ok, err = kv.Get([]byte("k2"))
	assert.True(t, string(val) == "v2" && ok && err == nil)

	// compact
	err = kv.Compact()
	require.Nil(t, err)

	_, ok, err = kv.Get([]byte("k1"))
	assert.True(t, !ok && err == nil)
	val, ok, err = kv.Get([]byte("k2"))
	assert.True(t, string(val) == "v2" && ok && err == nil)

	updated, err = kv.Set([]byte("k2"), []byte("v2"))
	assert.True(t, !updated && err == nil)

	updated, err = kv.Del([]byte("k3"))
	assert.True(t, updated && err == nil)
	_, ok, err = kv.Get([]byte("k3"))
	assert.True(t, !ok && err == nil)

	// reopen
	kv.Close()
	err = kv.Open()
	require.Nil(t, err)

	_, ok, err = kv.Get([]byte("k1"))
	assert.True(t, !ok && err == nil)
	val, ok, err = kv.Get([]byte("k2"))
	assert.True(t, string(val) == "v2" && ok && err == nil)
	_, ok, err = kv.Get([]byte("k3"))
	assert.True(t, !ok && err == nil)
}

func TestKVReopen(t *testing.T) {
	path := "test_db"
	defer os.RemoveAll(path)

	for mode := 0; mode < 3; mode++ {
		os.RemoveAll(path)
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
				assert.True(t, err == nil && ok && string(val) == string(key))
			}
		}

		err = kv.Close()
		require.Nil(t, err)
	}
}

func TestKVUpdateMode(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = "test_db"
	defer os.RemoveAll(kv.Options.Dirpath)

	os.RemoveAll(kv.Options.Dirpath)
	err := kv.Open()
	assert.Nil(t, err)
	defer kv.Close()

	updated, err := kv.SetEx([]byte("k1"), []byte("v1"), ModeUpdate)
	assert.True(t, !updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("v1"), ModeUpdate)
	assert.True(t, !updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("v1"), ModeInsert)
	assert.True(t, updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("xx"), ModeInsert)
	assert.True(t, !updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("yy"), ModeUpdate)
	assert.True(t, updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("zz"), ModeUpsert)
	assert.True(t, updated && err == nil)

	updated, err = kv.SetEx([]byte("k2"), []byte("tt"), ModeUpsert)
	assert.True(t, updated && err == nil)
}

func TestKVRecovery(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = "test_db"

	prepare := func() {
		os.RemoveAll(kv.Options.Dirpath)

		err := kv.Open()
		assert.Nil(t, err)
		defer kv.Close()

		updated, err := kv.Set([]byte("k1"), []byte("v1"))
		assert.True(t, updated && err == nil)

		tx := kv.NewTX()
		updated, err = tx.Set([]byte("k3"), []byte("v3"))
		assert.True(t, updated && err == nil)
		updated, err = tx.Set([]byte("k2"), []byte("v2"))
		assert.True(t, updated && err == nil)
		err = tx.Commit()
		require.Nil(t, err)
	}

	prepare()
	// simulate truncated log
	fp, _ := os.OpenFile(kv.log.FileName, os.O_RDWR, 0o644)
	st, _ := fp.Stat()
	fp.Truncate(st.Size() - 1)
	fp.Close()
	// reopen
	err := kv.Open()
	assert.Nil(t, err)
	// test
	val, ok, err := kv.Get([]byte("k1"))
	assert.True(t, string(val) == "v1" && ok && err == nil)
	_, ok, err = kv.Get([]byte("k2")) // bad
	assert.True(t, !ok && err == nil)
	_, ok, err = kv.Get([]byte("k3")) // bad
	assert.True(t, !ok && err == nil)
	kv.Close()

	prepare()
	// simulate bad checksum
	fp, _ = os.OpenFile(kv.log.FileName, os.O_RDWR, 0o644)
	st, _ = fp.Stat()
	fp.WriteAt([]byte{0}, st.Size()-1)
	fp.Close()
	// reopen
	err = kv.Open()
	assert.Nil(t, err)
	// test
	val, ok, err = kv.Get([]byte("k1"))
	assert.True(t, string(val) == "v1" && ok && err == nil)
	_, ok, err = kv.Get([]byte("k2")) // bad
	assert.True(t, !ok && err == nil)
	_, ok, err = kv.Get([]byte("k3")) // bad
	assert.True(t, !ok && err == nil)
	kv.Close()
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
	kv.Options.Dirpath = "test_db"
	defer os.RemoveAll(kv.Options.Dirpath)

	os.RemoveAll(kv.Options.Dirpath)
	err := kv.Open()
	assert.Nil(t, err)
	defer kv.Close()

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
	kv.Options.Dirpath = "test_db"
	defer os.RemoveAll(kv.Options.Dirpath)

	os.RemoveAll(kv.Options.Dirpath)
	err := kv.Open()
	require.Nil(t, err)
	defer kv.Close()

	updated, err := kv.Set([]byte("k1"), []byte("v1"))
	assert.True(t, updated && err == nil)
	updated, err = kv.Set([]byte("k2"), []byte("v2"))
	assert.True(t, updated && err == nil)

	tx1, tx2 := kv.NewTX(), kv.NewTX()
	val, ok, err := tx1.Get([]byte("k1"))
	assert.True(t, string(val) == "v1" && ok && err == nil)
	val, ok, err = tx1.Get([]byte("k2"))
	assert.True(t, string(val) == "v2" && ok && err == nil)

	updated, err = tx2.Del([]byte("k1"))
	assert.True(t, updated && err == nil)
	updated, err = tx2.Set([]byte("k2"), []byte("xxx"))
	assert.True(t, updated && err == nil)

	val, ok, err = tx1.Get([]byte("k1"))
	assert.True(t, string(val) == "v1" && ok && err == nil)
	val, ok, err = tx1.Get([]byte("k2"))
	assert.True(t, string(val) == "v2" && ok && err == nil)

	_, ok, err = tx2.Get([]byte("k1"))
	assert.True(t, !ok && err == nil)
	val, ok, err = tx2.Get([]byte("k2"))
	assert.True(t, string(val) == "xxx" && ok && err == nil)

	err = tx2.Commit()
	require.Nil(t, err)

	_, ok, err = kv.Get([]byte("k1"))
	assert.True(t, !ok && err == nil)
	val, ok, err = kv.Get([]byte("k2"))
	assert.True(t, string(val) == "xxx" && ok && err == nil)

	val, ok, err = tx1.Get([]byte("k1"))
	assert.True(t, string(val) == "v1" && ok && err == nil)
	val, ok, err = tx1.Get([]byte("k2"))
	assert.True(t, string(val) == "v2" && ok && err == nil)
	tx1.Abort()
}

func TestTXConflict(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = "test_db"
	defer os.RemoveAll(kv.Options.Dirpath)

	os.RemoveAll(kv.Options.Dirpath)
	err := kv.Open()
	require.Nil(t, err)
	defer kv.Close()

	updated, err := kv.Set([]byte("k1"), []byte("v1"))
	assert.True(t, updated && err == nil)

	tx1, tx2 := kv.NewTX(), kv.NewTX()
	updated, err = tx1.Set([]byte("k1"), []byte("x"))
	assert.True(t, updated && err == nil)
	updated, err = tx2.Set([]byte("k1"), []byte("y"))
	assert.True(t, updated && err == nil)

	err = tx1.Commit()
	assert.Nil(t, err)
	err = tx2.Commit()
	assert.Equal(t, ErrTXConflict, err)

	updated, err = kv.Set([]byte("k1"), []byte("z"))
	assert.True(t, updated && err == nil)
}

func TestKVAutoCompact(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = "test_db"
	kv.Options.LogShreshold = 1
	kv.Options.AutoCompact = true
	defer os.RemoveAll(kv.Options.Dirpath)

	os.RemoveAll(kv.Options.Dirpath)
	err := kv.Open()
	require.Nil(t, err)

	N := 20
	for i := 0; i < N; i++ {
		data := fmt.Sprintf("k%d", i)
		updated, err := kv.Set([]byte(data), []byte(data))
		assert.True(t, updated && err == nil)
	}
	kv.Close()

	time.Sleep(time.Millisecond * 10)
	assert.True(t, kv.version > 10)
}

func TestKVClosing(t *testing.T) {
	kv := KV{}
	kv.Options.Dirpath = "test_db"
	kv.Options.AutoCompact = true
	defer os.RemoveAll(kv.Options.Dirpath)

	os.RemoveAll(kv.Options.Dirpath)
	err := kv.Open()
	require.Nil(t, err)

	updated, err := kv.Set([]byte("k1"), []byte("v1"))
	require.True(t, updated && err == nil)

	t0 := time.Now()
	started := make(chan struct{}, 1)
	go func() {
		tx := kv.NewTX()
		defer tx.Abort()
		started <- struct{}{}
		time.Sleep(20 * time.Millisecond)
		val, ok, err := kv.Get([]byte("k1"))
		assert.True(t, err == nil && ok && string(val) == "v1")
	}()
	<-started

	err = kv.Close()
	require.Nil(t, err)
	duration := time.Since(t0).Milliseconds()
	assert.True(t, duration >= 20)
}

// QzBQWVJJOUhU https://trialofcode.org/
