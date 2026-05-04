package db0804

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMetadata(t *testing.T, reopen bool) {
	store := KVMetaStore{}
	store.slots[0].FileName = ".test_meta0"
	store.slots[1].FileName = ".test_meta1"
	defer os.Remove(store.slots[0].FileName)
	defer os.Remove(store.slots[1].FileName)

	err := store.Open()
	require.Nil(t, err)
	defer store.Close()

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
	testMetadata(t, false)
	testMetadata(t, true)
}

func testMetadataRecovery(t *testing.T, flag int) {
	store := KVMetaStore{}
	store.slots[0].FileName = ".test_meta0"
	store.slots[1].FileName = ".test_meta1"
	defer os.Remove(store.slots[0].FileName)
	defer os.Remove(store.slots[1].FileName)

	err := store.Open()
	require.Nil(t, err)
	defer store.Close()

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
	testMetadataRecovery(t, 0)
	testMetadataRecovery(t, 1)
}

// QzBQWVJJOUhU https://trialofcode.org/
