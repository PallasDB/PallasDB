package db

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMetaStore builds a store in a private directory. The slot files used to be
// fixed names in the package working directory, which made every test that
// touched metadata collide with every other one under -shuffle.
func newMetaStore(t *testing.T) *KVMetaStore {
	t.Helper()
	dir := t.TempDir()
	store := &KVMetaStore{}
	store.slots[0].FileName = filepath.Join(dir, "meta0")
	store.slots[1].FileName = filepath.Join(dir, "meta1")
	return store
}

func testMetadata(t *testing.T, reopen bool) {
	t.Helper()
	store := newMetaStore(t)

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

// damage corrupts a slot file in one of the ways a crash can.
func damage(t *testing.T, filename string, flag int) {
	t.Helper()
	fp, err := os.OpenFile(filename, os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer func() { assert.NoError(t, fp.Close()) }()

	st, err := fp.Stat()
	require.NoError(t, err)
	require.Greater(t, st.Size(), int64(8), "nothing to damage")

	switch flag {
	case 0: // flip the last payload byte: checksum mismatch
		_, err = fp.WriteAt([]byte{0}, st.Size()-1)
	case 1: // lose the tail: the payload no longer matches the size header
		err = fp.Truncate(st.Size() - 1)
	case 2: // lose everything but a stub: too short to hold a header
		err = fp.Truncate(4)
	case 3: // intact frame, unparseable payload
		err = writeMetaFileRaw(fp, []byte("{not json"))
	}
	require.NoError(t, err)
}

// writeMetaFileRaw frames an arbitrary payload with a valid header and a valid
// checksum, so that the only thing wrong with the slot is the payload itself.
func writeMetaFileRaw(fp *os.File, payload []byte) error {
	b := make([]byte, 8+len(payload))
	copy(b[8:], payload)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(payload)))
	binary.LittleEndian.PutUint32(b[0:4], crc32.ChecksumIEEE(b[4:]))
	if err := fp.Truncate(int64(len(b))); err != nil {
		return err
	}
	_, err := fp.WriteAt(b, 0)
	return err
}

func testMetadataRecovery(t *testing.T, flag int) {
	t.Helper()
	store := newMetaStore(t)

	err := store.Open()
	require.Nil(t, err)
	defer func() { assert.NoError(t, store.Close()) }()

	err = store.Set(KVMetaData{Version: 123})
	require.Nil(t, err)
	err = store.Set(KVMetaData{Version: 124})
	require.Nil(t, err)

	damaged := store.slots[store.current()].FileName
	require.NoError(t, store.Close())
	damage(t, damaged, flag)

	require.NoError(t, store.Open())
	assert.Equal(t, uint64(123), store.Get().Version,
		"the surviving slot must be used, not an empty snapshot")

	// The store heals: the next Set overwrites the damaged slot and a
	// subsequent open sees the newest version again.
	require.NoError(t, store.Set(KVMetaData{Version: 125}))
	require.NoError(t, store.Close())
	require.NoError(t, store.Open())
	assert.Equal(t, uint64(125), store.Get().Version)
}

func TestMetadataRecovery(t *testing.T) {
	t.Run("bad_checksum", func(t *testing.T) { testMetadataRecovery(t, 0) })
	t.Run("truncated", func(t *testing.T) { testMetadataRecovery(t, 1) })
	t.Run("stub", func(t *testing.T) { testMetadataRecovery(t, 2) })
	t.Run("bad_json", func(t *testing.T) { testMetadataRecovery(t, 3) })
}

// readMetaFile has to say *which* kind of unusable a slot is. Reporting every
// failure as "empty, no error" is what let a damaged store open as a blank one.
func TestReadMetaFileDistinguishesAbsentFromCorrupt(t *testing.T) {
	for _, tc := range []struct {
		name string
		flag int
	}{
		{"bad_checksum", 0},
		{"truncated", 1},
		{"stub", 2},
		{"bad_json", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newMetaStore(t)
			require.NoError(t, store.Open())
			require.NoError(t, store.Set(KVMetaData{Version: 7}))
			name := store.slots[store.current()].FileName
			require.NoError(t, store.Close())
			damage(t, name, tc.flag)

			fp, err := os.OpenFile(name, os.O_RDWR, 0o644)
			require.NoError(t, err)
			defer func() { assert.NoError(t, fp.Close()) }()

			_, err = readMetaFile(fp)
			assert.ErrorIs(t, err, ErrMetaCorrupt)
		})
	}

	t.Run("absent", func(t *testing.T) {
		name := filepath.Join(t.TempDir(), "meta0")
		require.NoError(t, os.WriteFile(name, nil, 0o644))
		fp, err := os.OpenFile(name, os.O_RDWR, 0o644)
		require.NoError(t, err)
		defer func() { assert.NoError(t, fp.Close()) }()

		data, err := readMetaFile(fp)
		assert.ErrorIs(t, err, errMetaAbsent)
		assert.NotErrorIs(t, err, ErrMetaCorrupt)
		assert.Equal(t, KVMetaData{}, data)
	})
}

// Both slots gone is not "a fresh database". If SSTables are still on disk,
// opening anyway would report version 0 with no tables, and the next compaction
// would renumber straight over the surviving files.
func TestMetadataBothSlotsCorruptFailsOpen(t *testing.T) {
	store := newMetaStore(t)
	dir := filepath.Dir(store.slots[0].FileName)

	require.NoError(t, store.Open())
	require.NoError(t, store.Set(KVMetaData{Version: 1, SSTables: []string{"sstable_1"}}))
	require.NoError(t, store.Set(KVMetaData{Version: 2, SSTables: []string{"sstable_2", "sstable_1"}}))
	require.NoError(t, store.Close())

	// The data the metadata points at is still there; only the map is gone.
	for _, name := range []string{"sstable_1", "sstable_2"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("data"), 0o644))
	}
	damage(t, store.slots[0].FileName, 0)
	damage(t, store.slots[1].FileName, 1)

	err := store.Open()
	require.ErrorIs(t, err, ErrMetaUnreadable)
	require.ErrorIs(t, err, ErrMetaCorrupt, "the error must name the underlying cause")
	assert.Contains(t, err.Error(), "sstable_")

	// A failed Open leaves nothing open behind it.
	assert.Empty(t, store.MultiClosers)
}

// The same situation with no data files really is a fresh database: there is
// nothing to lose, so refusing to open would be obstruction rather than safety.
func TestMetadataBothSlotsAbsentOpensFresh(t *testing.T) {
	store := newMetaStore(t)
	require.NoError(t, store.Open())
	defer func() { assert.NoError(t, store.Close()) }()
	assert.Equal(t, KVMetaData{}, store.Get())
}

func TestMetadataCorruptButEmptyDirectoryOpens(t *testing.T) {
	store := newMetaStore(t)
	require.NoError(t, store.Open())
	require.NoError(t, store.Set(KVMetaData{Version: 1}))
	require.NoError(t, store.Set(KVMetaData{Version: 2}))
	require.NoError(t, store.Close())

	damage(t, store.slots[0].FileName, 0)
	damage(t, store.slots[1].FileName, 0)

	// No SSTables were ever written, so version 0 with no tables is an honest
	// description of what is on disk.
	require.NoError(t, store.Open())
	defer func() { assert.NoError(t, store.Close()) }()
	assert.Equal(t, KVMetaData{}, store.Get())
}

// A slot that fails to reach disk must not be believed just because it was
// written recently.
func TestMetadataSetSyncFailureIsReported(t *testing.T) {
	store := newMetaStore(t)
	fs := withFaultFS(t, matchBase("meta0", "meta1"))
	require.NoError(t, store.Open())
	defer func() { assert.NoError(t, store.Close()) }()

	require.NoError(t, store.Set(KVMetaData{Version: 1}))

	fs.arm(faultSpec{op: opSync, at: 1, mode: faultFail})
	require.Error(t, store.Set(KVMetaData{Version: 2}))
	require.True(t, fs.hasFired())
	fs.disarm()

	assert.Equal(t, uint64(1), store.Get().Version, "a failed Set must not be visible")
}
