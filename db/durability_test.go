package db

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Crash-consistency tests for the flush path. A flush is three durable steps in
// a fixed order -- write and fsync the SSTable, fsync the metadata snapshot
// that names it, reset the WAL -- and the store has to survive losing power
// between any two of them. The fault harness in fault_test.go is what makes
// each of those boundaries reachable from a test.

func openKV(t *testing.T, dir string) *KV {
	t.Helper()
	kv := &KV{}
	kv.Options.Dirpath = dir
	kv.Options.LogThreshold = 1 // every Compact flushes
	require.NoError(t, kv.Open())
	return kv
}

func mustGet(t *testing.T, kv *KV, key string) (string, bool) {
	t.Helper()
	val, ok, err := kv.Get([]byte(key))
	require.NoError(t, err)
	return string(val), ok
}

func requireAbsent(t *testing.T, kv *KV, key string) {
	t.Helper()
	val, ok := mustGet(t, kv, key)
	assert.False(t, ok, "key %q must stay deleted, got %q", key, val)
}

func requireValue(t *testing.T, kv *KV, key, want string) {
	t.Helper()
	val, ok := mustGet(t, kv, key)
	require.True(t, ok, "key %q must be present", key)
	assert.Equal(t, want, val)
}

func sstableCount(t *testing.T, dir string) int {
	t.Helper()
	n, err := countSSTables(dir)
	require.NoError(t, err)
	return n
}

// The resurrection regression, end to end through the public API.
//
// Write, flush, then lose the WAL truncate: the reset is acknowledged but the
// old records stay on disk with their CRC32s intact. The next transaction
// deletes the key and, being exactly the size of the first stale transaction,
// leaves the second one starting precisely on a record boundary. Replay used to
// walk straight into it, find its EntryCommit, and bring the deleted key back.
func TestFlushWithNonDurableTruncateKeepsKeyDeleted(t *testing.T) {
	dir := t.TempDir()
	fs := withFaultFS(t, matchBase("kv_log"))
	kv := openKV(t, dir)

	// Size the transactions so the post-truncate write ends exactly where the
	// stale second transaction begins. Both come to the same number of bytes:
	//   tx1: [set key=v] [commit]            = 2*hdr + |key| + |v|
	//   tx3: [set k2=pad] [del key] [commit] = 3*hdr + |k2| + |pad| + |key|
	const key, k2, padLen = "key", "k2", 17
	pad := string(bytes.Repeat([]byte("p"), padLen))
	v1 := string(bytes.Repeat([]byte("1"), entryHeaderSize+len(k2)+padLen))
	v2 := string(bytes.Repeat([]byte("2"), len(v1)))

	_, err := kv.Set([]byte(key), []byte(v1))
	require.NoError(t, err)
	_, err = kv.Set([]byte(key), []byte(v2))
	require.NoError(t, err)

	logFile := filepath.Join(dir, "kv_log")
	before, err := os.ReadFile(logFile)
	require.NoError(t, err)

	// Flush. The SSTable and the metadata land; the WAL reset does not.
	fs.arm(faultSpec{op: opTruncate, at: 1, mode: faultDrop})
	require.NoError(t, kv.Compact())
	require.True(t, fs.hasFired(), "the WAL truncate fault must have fired")
	fs.disarm()
	require.Equal(t, 1, sstableCount(t, dir))

	after, err := os.ReadFile(logFile)
	require.NoError(t, err)
	require.Len(t, after, len(before), "the stale records must still be on disk")

	// Delete the key, plus a filler write that makes the transaction land on
	// the old record boundary.
	tx := kv.NewTX()
	deleted, err := tx.Del([]byte(key))
	require.NoError(t, err)
	require.True(t, deleted)
	_, err = tx.Set([]byte(k2), []byte(pad))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// The setup only proves something if a well-formed stale record really is
	// sitting at the new commit point.
	raw, err := os.ReadFile(logFile)
	require.NoError(t, err)
	require.Len(t, raw, len(before))
	staleAt := logHeaderSize + (len(before)-logHeaderSize)/2
	stale := Entry{}
	require.NoError(t, stale.Decode(bytes.NewReader(raw[staleAt:])),
		"test setup: the leftover must decode cleanly, or nothing is being proven")
	require.Equal(t, []byte(key), stale.key)
	require.Equal(t, []byte(v2), stale.val)

	require.NoError(t, kv.Close())

	reopened := openKV(t, dir)
	defer func() { assert.NoError(t, reopened.Close()) }()
	requireAbsent(t, reopened, key)
	requireValue(t, reopened, k2, pad)
}

// Crash between the SSTable fsync and the metadata fsync. The new SSTable is on
// disk but nothing points at it, and the WAL was never reset -- so every write
// must still be there, recovered from the log against the previous snapshot.
func TestCrashBetweenSSTableAndMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode faultMode
	}{
		{"metadata write fails", faultFail},
		{"metadata write torn", faultShort},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fs := withFaultFS(t, matchBase("meta0", "meta1"))
			kv := openKV(t, dir)

			// An established store: one snapshot already on disk.
			_, err := kv.Set([]byte("a"), []byte("v-a"))
			require.NoError(t, err)
			require.NoError(t, kv.Compact())

			for _, k := range []string{"b", "c"} {
				_, err := kv.Set([]byte(k), []byte("v-"+k))
				require.NoError(t, err)
			}
			_, err = kv.Del([]byte("b"))
			require.NoError(t, err)

			fs.arm(faultSpec{op: opWriteAt, at: 1, mode: tc.mode})
			require.Error(t, kv.Compact(), "the flush must report the failure")
			require.True(t, fs.hasFired())
			fs.disarm()
			require.NoError(t, kv.Close())

			reopened := openKV(t, dir)
			defer func() { assert.NoError(t, reopened.Close()) }()
			requireValue(t, reopened, "a", "v-a")
			requireAbsent(t, reopened, "b")
			requireValue(t, reopened, "c", "v-c")
		})
	}
}

// The same failure on a brand-new store leaves an orphan SSTable and no
// metadata at all. Opening then has two possible readings -- "orphan from a
// failed first flush, safe to start fresh" and "someone deleted the metadata of
// a real database" -- and nothing on disk distinguishes them. The store refuses
// rather than guess, because guessing wrong renumbers over live data. The cost
// is that a first flush that fails needs an operator to remove one file; the
// error names it.
func TestFirstFlushMetadataFailureRefusesToOpen(t *testing.T) {
	dir := t.TempDir()
	fs := withFaultFS(t, matchBase("meta0", "meta1"))
	kv := openKV(t, dir)

	_, err := kv.Set([]byte("a"), []byte("v-a"))
	require.NoError(t, err)

	fs.arm(faultSpec{op: opWriteAt, at: 1, mode: faultFail})
	require.Error(t, kv.Compact())
	require.True(t, fs.hasFired())
	fs.disarm()
	require.NoError(t, kv.Close())
	require.Equal(t, 1, sstableCount(t, dir), "an orphan SSTable is on disk")

	broken := KV{}
	broken.Options.Dirpath = dir
	require.ErrorIs(t, broken.Open(), ErrMetaUnreadable)

	// Once the orphan is gone the store opens and the WAL still holds the data:
	// the failure was loud, not fatal.
	require.NoError(t, os.Remove(filepath.Join(dir, "sstable_1")))
	recovered := openKV(t, dir)
	defer func() { assert.NoError(t, recovered.Close()) }()
	requireValue(t, recovered, "a", "v-a")
}

// Crash between the metadata fsync and the WAL reset. The flush is complete as
// far as the store is concerned; the log is either stale or gone. Either way
// the contents must be exactly what was committed -- no duplicates, no
// resurrected deletes.
func TestCrashBetweenMetadataAndWALTruncate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      faultMode
		compactOK bool
	}{
		// Acknowledged but never applied: the classic lying truncate.
		{"truncate silently dropped", faultDrop, true},
		// Reported failure: the flush errors out, but the SSTable and the
		// metadata naming it are already durable.
		{"truncate fails", faultFail, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			fs := withFaultFS(t, matchBase("kv_log"))
			kv := openKV(t, dir)

			for _, k := range []string{"a", "b", "c"} {
				_, err := kv.Set([]byte(k), []byte("v-"+k))
				require.NoError(t, err)
			}
			_, err := kv.Del([]byte("b"))
			require.NoError(t, err)

			fs.arm(faultSpec{op: opTruncate, at: 1, mode: tc.mode})
			err = kv.Compact()
			if tc.compactOK {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.True(t, fs.hasFired())
			fs.disarm()
			require.Equal(t, 1, sstableCount(t, dir))
			require.NoError(t, kv.Close())

			reopened := openKV(t, dir)
			defer func() { assert.NoError(t, reopened.Close()) }()
			requireValue(t, reopened, "a", "v-a")
			requireAbsent(t, reopened, "b")
			requireValue(t, reopened, "c", "v-c")

			// And the store keeps working afterwards.
			_, err = reopened.Set([]byte("d"), []byte("v-d"))
			require.NoError(t, err)
			requireValue(t, reopened, "d", "v-d")
		})
	}
}

// A write committed after a flush must not be fenced off by the epoch that
// flush just raised.
func TestWritesAfterFlushSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	kv := openKV(t, dir)

	_, err := kv.Set([]byte("a"), []byte("1"))
	require.NoError(t, err)
	require.NoError(t, kv.Compact())
	_, err = kv.Set([]byte("b"), []byte("2"))
	require.NoError(t, err)
	_, err = kv.Del([]byte("a"))
	require.NoError(t, err)
	require.NoError(t, kv.Close())

	reopened := openKV(t, dir)
	defer func() { assert.NoError(t, reopened.Close()) }()
	requireAbsent(t, reopened, "a")
	requireValue(t, reopened, "b", "2")
}

// Losing both metadata slots on a store that still has SSTables must not open
// as an empty database: the next compaction would renumber over them.
func TestKVBothMetaSlotsCorruptFailsOpen(t *testing.T) {
	dir := t.TempDir()
	kv := openKV(t, dir)

	// Two flushes, so that both slots have been written and can be damaged.
	for _, k := range []string{"a", "b"} {
		_, err := kv.Set([]byte(k), []byte("v-"+k))
		require.NoError(t, err)
		require.NoError(t, kv.Compact())
	}
	require.NoError(t, kv.Close())
	tables := sstableCount(t, dir)
	require.Positive(t, tables)

	damage(t, filepath.Join(dir, "meta0"), 0)
	damage(t, filepath.Join(dir, "meta1"), 1)

	broken := KV{}
	broken.Options.Dirpath = dir
	err := broken.Open()
	require.ErrorIs(t, err, ErrMetaUnreadable)
	require.ErrorIs(t, err, ErrMetaCorrupt)
	assert.Equal(t, tables, sstableCount(t, dir), "the data files must still be there")
}

// A damaged stale slot is harmless: the live snapshot is still readable, the
// store opens on it, and the next flush overwrites the damaged slot.
//
// The sizes matter. A small second flush would trip (*KV).shouldMerge and the
// merge would delete the SSTables the older snapshot names, so this test keeps
// the levels far enough apart that only the log flush runs.
func TestKVStaleMetaSlotCorruptRecovers(t *testing.T) {
	dir := t.TempDir()
	kv := openKV(t, dir)

	for i := range 64 {
		_, err := kv.Set(fmt.Appendf(nil, "k%03d", i), []byte("bulk"))
		require.NoError(t, err)
	}
	require.NoError(t, kv.Compact())
	stale := kv.meta.slots[kv.meta.current()].FileName

	_, err := kv.Set([]byte("late"), []byte("value"))
	require.NoError(t, err)
	require.NoError(t, kv.Compact())
	require.Equal(t, 2, sstableCount(t, dir), "no merge should have run")
	require.NotEqual(t, stale, kv.meta.slots[kv.meta.current()].FileName)
	require.NoError(t, kv.Close())

	damage(t, stale, 0)

	reopened := openKV(t, dir)
	defer func() { assert.NoError(t, reopened.Close()) }()
	requireValue(t, reopened, "k000", "bulk")
	requireValue(t, reopened, "late", "value")

	// The damaged slot heals on the next flush and survives a restart.
	_, err = reopened.Set([]byte("later"), []byte("3"))
	require.NoError(t, err)
	require.NoError(t, reopened.Compact())
	require.NoError(t, reopened.Close())

	again := openKV(t, dir)
	defer func() { assert.NoError(t, again.Close()) }()
	requireValue(t, again, "k000", "bulk")
	requireValue(t, again, "late", "value")
	requireValue(t, again, "later", "3")
}
