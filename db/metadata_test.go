package db

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMetadata(t *testing.T, reopen bool) {
	t.Helper()
	store := KVMetaStore{}
	store.slots[0].FileName = ".test_meta0"
	store.slots[1].FileName = ".test_meta1"
	t.Cleanup(func() {
		_ = os.Remove(store.slots[0].FileName)
		_ = os.Remove(store.slots[1].FileName)
	})

	err := store.Open()
	require.Nil(t, err)
	defer func() { assert.NoError(t, store.Close()) }()

	for i := uint64(1); i < 10; i++ {
		if reopen {
			err = store.Close()
			require.Nil(t, err)
			err = store.Open()
			require.Nil(t, err)
		}

		meta := store.Get()
		assert.Equal(t, i-1, meta.Version)
		err = store.Set(KVMetaData{Version: i})
		require.Nil(t, err)
		meta = store.Get()
		assert.Equal(t, i, meta.Version)
	}
}

func TestMetadata(t *testing.T) {
	t.Run("no_reopen", func(t *testing.T) { testMetadata(t, false) })
	t.Run("reopen", func(t *testing.T) { testMetadata(t, true) })
}

func testMetadataRecovery(t *testing.T, flag int) {
	t.Helper()
	store := KVMetaStore{}
	store.slots[0].FileName = ".test_meta0"
	store.slots[1].FileName = ".test_meta1"
	t.Cleanup(func() {
		_ = os.Remove(store.slots[0].FileName)
		_ = os.Remove(store.slots[1].FileName)
	})

	err := store.Open()
	require.Nil(t, err)
	defer func() { assert.NoError(t, store.Close()) }()

	err = store.Set(KVMetaData{Version: 123})
	require.Nil(t, err)
	err = store.Set(KVMetaData{Version: 124})
	require.Nil(t, err)

	fp := store.slots[store.current()].fp
	st, err := fp.Stat()
	require.Nil(t, err)
	if flag == 0 {
		_, err = fp.WriteAt([]byte{0}, st.Size()-1)
	} else {
		err = fp.Truncate(st.Size() - 1)
	}
	require.Nil(t, err)

	err = store.Close()
	require.Nil(t, err)
	err = store.Open()
	require.Nil(t, err)
	meta := store.Get()
	assert.Equal(t, uint64(123), meta.Version)
}

func TestMetadataRecovery(t *testing.T) {
	t.Run("bad_checksum", func(t *testing.T) { testMetadataRecovery(t, 0) })
	t.Run("truncated", func(t *testing.T) { testMetadataRecovery(t, 1) })
}

// QzBQWVJJOUhU https://trialofcode.org/
