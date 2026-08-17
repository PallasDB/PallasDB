package db

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scanTX walks every live key visible to tx.
func scanTX(tx *KVTX) ([]string, error) {
	iter, err := tx.Seek(nil)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for iter.Valid() {
		out = append(out, string(iter.Key()))
		if err := iter.Next(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// listSSTables returns the sstable_* entries currently present in dir.
func listSSTables(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := []string{}
	for _, ent := range entries {
		if !ent.IsDir() && strings.HasPrefix(ent.Name(), sstablePrefix) {
			out = append(out, ent.Name())
		}
	}
	return out
}

// TestKVSnapshotSurvivesCompaction is the regression test for the SortedFile
// use-after-close race: transactions capture the SSTable list by reference and
// the background compactor retires, closes and unlinks those very files while
// the transactions are still reading them.
//
// The reader goroutines assert snapshot isolation as well: a transaction must
// keep seeing exactly the keys that existed when it started, no matter how many
// flushes and merges happen underneath it.
func TestKVSnapshotSurvivesCompaction(t *testing.T) {
	const (
		writers   = 3
		perWriter = 40
		readers   = 2
		rescans   = 12
	)

	kv := &KV{Options: KVOptions{
		Dirpath:      t.TempDir(),
		LogShreshold: 8,
		GrowthFactor: 2,
		AutoCompact:  true,
	}}
	require.NoError(t, kv.Open())

	// Seed enough data that the snapshots below have real SSTables to hold on to.
	for i := 0; i < 24; i++ {
		key := []byte(fmt.Sprintf("seed%03d", i))
		_, err := kv.Set(key, key)
		require.NoError(t, err)
	}
	require.NoError(t, kv.Compact())
	require.NoError(t, kv.Compact())

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := []byte(fmt.Sprintf("w%d-%04d", w, i))
				if _, err := kv.Set(key, key); !assert.NoError(t, err) {
					return
				}
			}
		}(w)
	}

	// Long-lived snapshots: each spans many flushes and merges.
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx := kv.NewTX()
			defer tx.Abort()

			want, err := scanTX(tx)
			if !assert.NoError(t, err) {
				return
			}
			assert.NotEmpty(t, want)
			for i := 0; i < rescans; i++ {
				got, err := scanTX(tx)
				if !assert.NoError(t, err) {
					return
				}
				if !assert.Equal(t, want, got, "snapshot changed under the transaction") {
					return
				}
				runtime.Gosched()
			}
		}()
	}

	// A caller-driven compaction racing the background one.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			assert.NoError(t, kv.Compact())
			runtime.Gosched()
		}
	}()

	wg.Wait()
	assert.NoError(t, kv.LastCompactError())

	// Everything that was written is still readable.
	for w := 0; w < writers; w++ {
		for i := 0; i < perWriter; i++ {
			key := []byte(fmt.Sprintf("w%d-%04d", w, i))
			val, ok, err := kv.Get(key)
			require.NoError(t, err)
			require.True(t, ok, "missing key %s", key)
			require.Equal(t, key, val)
		}
	}
	require.NoError(t, kv.Close())
}

// TestKVConcurrentCompact checks that Compact called from several goroutines at
// once (the CLI can do this while the background compactor runs) neither
// corrupts the version counter nor drops an SSTable.
func TestKVConcurrentCompact(t *testing.T) {
	dir := t.TempDir()
	kv := &KV{Options: KVOptions{Dirpath: dir, LogShreshold: 4, GrowthFactor: 2}}
	require.NoError(t, kv.Open())

	const nkeys = 64
	for i := 0; i < nkeys; i++ {
		key := []byte(fmt.Sprintf("key%03d", i))
		_, err := kv.Set(key, key)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				assert.NoError(t, kv.Compact())
			}
		}()
	}
	wg.Wait()

	// The in-memory level stack, the metadata and the directory must agree.
	kv.mu.Lock()
	names := make([]string, len(kv.main))
	for i, file := range kv.main {
		names[i] = filepath.Base(file.FileName)
	}
	version := kv.version
	kv.mu.Unlock()

	meta := kv.meta.Get()
	assert.Equal(t, meta.SSTables, names, "metadata and level stack disagree")
	assert.Equal(t, meta.Version, version, "metadata and in-memory version disagree")
	assert.NotZero(t, version)

	// No transaction is open, so every retired table must already be unlinked
	// and every referenced one must still be present.
	assert.ElementsMatch(t, meta.SSTables, listSSTables(t, dir), "leaked or missing SSTable files")

	for i := 0; i < nkeys; i++ {
		key := []byte(fmt.Sprintf("key%03d", i))
		val, ok, err := kv.Get(key)
		require.NoError(t, err)
		require.True(t, ok, "missing key %s", key)
		require.Equal(t, key, val)
	}
	require.NoError(t, kv.Close())

	// And the same data survives a reopen from that metadata.
	require.NoError(t, kv.Open())
	for i := 0; i < nkeys; i++ {
		key := []byte(fmt.Sprintf("key%03d", i))
		_, ok, err := kv.Get(key)
		require.NoError(t, err)
		require.True(t, ok, "missing key %s after reopen", key)
	}
	require.NoError(t, kv.Close())
}

// blockSSTableNames makes the next few SSTable names unusable by squatting on
// them with non-empty directories, so any attempt to create one fails.
func blockSSTableNames(t *testing.T, dir string, from, to uint64) {
	t.Helper()
	for v := from; v <= to; v++ {
		blocker := filepath.Join(dir, sstableName(v))
		require.NoError(t, os.Mkdir(blocker, 0o755))
		// Non-empty, so the cleanup path's os.Remove cannot clear it either.
		require.NoError(t, os.WriteFile(filepath.Join(blocker, "keep"), []byte("x"), 0o644))
	}
}

// TestKVCompactErrorIsObservable pins down that a failed compaction is surfaced
// rather than swallowed, on both the caller-driven and the background path, and
// that a failed flush does not lose the writes it froze.
func TestKVCompactErrorIsObservable(t *testing.T) {
	t.Run("caller driven", func(t *testing.T) {
		dir := t.TempDir()
		logged := make(chan error, 8)
		kv, err := NewKV(dir,
			WithLogThreshold(1),
			WithErrorLogger(func(e error) {
				select {
				case logged <- e:
				default:
				}
			}),
		)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		_, err = kv.Set([]byte("k"), []byte("v"))
		require.NoError(t, err)

		blockSSTableNames(t, dir, 1, 4)

		err = kv.Compact()
		require.Error(t, err, "compaction should fail while the SSTable name is squatted")
		assert.Equal(t, err, kv.LastCompactError())

		select {
		case e := <-logged:
			assert.Error(t, e)
		default:
			t.Fatal("error logger was never called")
		}

		// The failed flush rolled back: the write is still readable.
		val, ok, err := kv.Get([]byte("k"))
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, []byte("v"), val)
	})

	t.Run("background thread", func(t *testing.T) {
		dir := t.TempDir()
		logged := make(chan error, 8)
		blockSSTableNames(t, dir, 1, 8)

		kv, err := NewKV(dir,
			WithLogThreshold(1),
			WithAutoCompact(true),
			WithErrorLogger(func(e error) {
				select {
				case logged <- e:
				default:
				}
			}),
		)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, kv.Close()) })

		_, err = kv.Set([]byte("k"), []byte("v"))
		require.NoError(t, err)

		select {
		case e := <-logged:
			assert.Error(t, e)
		case <-time.After(30 * time.Second):
			t.Fatal("background compaction error was swallowed")
		}
		assert.Error(t, kv.LastCompactError())
	})
}

// TestKVAfterCloseDoesNotHang covers the post-Close hazards: a second Close, and
// operations issued after Close, must return an error promptly instead of
// blocking on a channel nobody drains or panicking on a reused WaitGroup.
func TestKVAfterCloseDoesNotHang(t *testing.T) {
	t.Run("operations return ErrKVClosed", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 1, AutoCompact: true}}
		require.NoError(t, kv.Open())
		_, err := kv.Set([]byte("k"), []byte("v"))
		require.NoError(t, err)

		require.NoError(t, kv.Close())
		require.NoError(t, kv.Close(), "Close must be idempotent")

		done := make(chan struct{})
		go func() {
			defer close(done)

			_, err := kv.Set([]byte("k2"), []byte("v2"))
			assert.ErrorIs(t, err, ErrKVClosed)

			_, _, err = kv.Get([]byte("k"))
			assert.ErrorIs(t, err, ErrKVClosed)

			_, err = kv.Del([]byte("k"))
			assert.ErrorIs(t, err, ErrKVClosed)

			_, _, err = kv.IterAll()
			assert.ErrorIs(t, err, ErrKVClosed)

			assert.ErrorIs(t, kv.Compact(), ErrKVClosed)

			tx := kv.NewTX()
			assert.ErrorIs(t, tx.Err(), ErrKVClosed)
			_, err = tx.Set([]byte("k3"), []byte("v3"))
			assert.ErrorIs(t, err, ErrKVClosed)
			assert.ErrorIs(t, tx.Commit(), ErrKVClosed)
			tx.Abort()
		}()

		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Fatal("an operation after Close blocked instead of returning an error")
		}
	})

	t.Run("NewTX racing Close", func(t *testing.T) {
		kv := &KV{Options: KVOptions{Dirpath: t.TempDir(), LogShreshold: 4, AutoCompact: true}}
		require.NoError(t, kv.Open())

		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < 40; i++ {
					tx := kv.NewTX()
					// Either a live snapshot or a rejected one; never a panic
					// and never a nil dereference.
					if _, _, err := tx.Get([]byte("k")); err != nil {
						assert.ErrorIs(t, err, ErrKVClosed)
					}
					tx.Abort()
					runtime.Gosched()
				}
			}()
		}

		closed := make(chan error, 1)
		go func() { closed <- kv.Close() }()

		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(30 * time.Second):
			t.Fatal("Close blocked while transactions were being opened")
		}
		wg.Wait()
	})
}

// TestKVOpenSweepsOrphanSSTables covers the crash-between-write-and-metadata
// case: files nothing references must not accumulate in the data directory.
func TestKVOpenSweepsOrphanSSTables(t *testing.T) {
	dir := t.TempDir()
	kv := &KV{Options: KVOptions{Dirpath: dir, LogShreshold: 1}}
	require.NoError(t, kv.Open())
	for i := 0; i < 8; i++ {
		key := []byte(fmt.Sprintf("k%d", i))
		_, err := kv.Set(key, key)
		require.NoError(t, err)
	}
	require.NoError(t, kv.Compact())
	require.NoError(t, kv.Close())

	referenced := kv.meta.Get().SSTables
	require.NotEmpty(t, referenced)

	orphan := filepath.Join(dir, sstableName(9999))
	require.NoError(t, os.WriteFile(orphan, []byte("garbage"), 0o644))

	require.NoError(t, kv.Open())
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	_, err := os.Stat(orphan)
	assert.ErrorIs(t, err, os.ErrNotExist, "unreferenced SSTable was not swept")
	assert.ElementsMatch(t, referenced, listSSTables(t, dir))

	for i := 0; i < 8; i++ {
		key := []byte(fmt.Sprintf("k%d", i))
		_, ok, err := kv.Get(key)
		require.NoError(t, err)
		assert.True(t, ok, "sweep removed a live SSTable")
	}
}

// TestKVCacheInvalidatedByTX pins that a write committed through a KVTX (the
// path the SQL layer takes for every row) invalidates the read cache; otherwise
// KV.Get would keep serving the pre-commit value.
func TestKVCacheInvalidatedByTX(t *testing.T) {
	kv, err := NewKV(t.TempDir(), WithCache(8*1024*1024, 100_000))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, kv.Close()) })

	_, err = kv.Set([]byte("k"), []byte("old"))
	require.NoError(t, err)
	val, ok, err := kv.Get([]byte("k")) // warms the cache
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []byte("old"), val)
	kv.cache.Wait()

	tx := kv.NewTX()
	_, err = tx.Set([]byte("k"), []byte("new"))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	val, ok, err = kv.Get([]byte("k"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("new"), val)

	tx = kv.NewTX()
	_, err = tx.Del([]byte("k"))
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	_, ok, err = kv.Get([]byte("k"))
	require.NoError(t, err)
	assert.False(t, ok)
}
