package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/teddymalhan/pallasdb/db"
)

// seedLocalStore writes entries directly through the storage engine so the
// dump path is exercised against real on-disk data.
func seedLocalStore(t *testing.T, dataDir string, entries map[string]string) {
	t.Helper()

	store, err := db.NewKV(dataDir)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	for key, value := range entries {
		_, err := store.SetEx([]byte(key), []byte(value), db.ModeUpsert)
		require.NoError(t, err)
	}
}

func readLocalStore(t *testing.T, dataDir string) map[string]string {
	t.Helper()

	store, err := db.NewKV(dataDir)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()

	iter, release, err := store.IterAll()
	require.NoError(t, err)
	defer release()

	out := map[string]string{}
	for iter.Valid() {
		out[string(iter.Key())] = string(iter.Val())
		require.NoError(t, iter.Next())
	}
	return out
}

func TestDumpRestoreLocalRoundTrip(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	backup := filepath.Join(dir, "backup.pallas")

	entries := map[string]string{
		"alpha":             "one",
		"bravo":             "two",
		"charlie":           "three",
		"binary\x00key":     "binary\x00value",
		"empty-value":       "",
		"a-much-longer-key": string(bytes.Repeat([]byte("x"), 4096)),
	}
	seedLocalStore(t, source, entries)

	stdout, stderr, err := executeCommand(t, "dump", "--data-dir", source, "--output", backup)
	require.NoError(t, err)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "dumped 6 keys")

	stdout, stderr, err = executeCommand(t, "restore", "--data-dir", target, "--input", backup)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "restored 6 keys\n", stdout)

	require.Equal(t, entries, readLocalStore(t, target))
}

func TestDumpWritesStreamToStdoutAndRestoreReadsStdin(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	seedLocalStore(t, source, map[string]string{"k": "v"})

	stream, _, err := executeCommand(t, "dump", "--data-dir", source)
	require.NoError(t, err)
	require.True(t, bytes.HasPrefix([]byte(stream), dumpMagic))

	stdout, _, err := executeCommandWithInput(t, stream, "restore", "--data-dir", target)
	require.NoError(t, err)
	require.Equal(t, "restored 1 keys\n", stdout)
	require.Equal(t, map[string]string{"k": "v"}, readLocalStore(t, target))
}

func TestRestoreOverwritesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	backup := filepath.Join(dir, "backup.pallas")

	seedLocalStore(t, source, map[string]string{"k": "new"})
	seedLocalStore(t, target, map[string]string{"k": "old", "survivor": "yes"})

	_, _, err := executeCommand(t, "dump", "--data-dir", source, "--output", backup)
	require.NoError(t, err)
	_, _, err = executeCommand(t, "restore", "--data-dir", target, "--input", backup)
	require.NoError(t, err)

	require.Equal(t, map[string]string{"k": "new", "survivor": "yes"}, readLocalStore(t, target))
}

func TestDumpRestoreRoundTripAcrossBatchBoundary(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	backup := filepath.Join(dir, "backup.pallas")

	entries := make(map[string]string, restoreBatchSize+7)
	for i := range restoreBatchSize + 7 {
		entries[keyForIndex(i)] = valueForIndex(i)
	}
	seedLocalStore(t, source, entries)

	_, _, err := executeCommand(t, "dump", "--data-dir", source, "--output", backup)
	require.NoError(t, err)
	stdout, _, err := executeCommand(t, "restore", "--data-dir", target, "--input", backup)
	require.NoError(t, err)
	require.Contains(t, stdout, "restored 1007 keys")

	require.Equal(t, entries, readLocalStore(t, target))
}

func TestRestoreRejectsTruncatedStream(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	backup := filepath.Join(dir, "backup.pallas")
	seedLocalStore(t, source, map[string]string{"a": "1", "b": "2"})

	_, _, err := executeCommand(t, "dump", "--data-dir", source, "--output", backup)
	require.NoError(t, err)

	full, err := os.ReadFile(backup)
	require.NoError(t, err)
	truncated := filepath.Join(dir, "truncated.pallas")
	require.NoError(t, os.WriteFile(truncated, full[:len(full)-3], 0o600))

	_, _, err = executeCommand(t, "restore", "--data-dir", target, "--input", truncated)
	require.Error(t, err)
	require.ErrorIs(t, err, errDumpCorrupt)
}

func TestRestoreRejectsCorruptedPayload(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	target := filepath.Join(dir, "target")
	backup := filepath.Join(dir, "backup.pallas")
	seedLocalStore(t, source, map[string]string{"alpha": "bravo"})

	_, _, err := executeCommand(t, "dump", "--data-dir", source, "--output", backup)
	require.NoError(t, err)

	full, err := os.ReadFile(backup)
	require.NoError(t, err)
	// Flip a byte inside the value; the record framing stays intact so only the
	// checksum can catch it.
	full[len(full)-8] ^= 0xFF
	corrupted := filepath.Join(dir, "corrupt.pallas")
	require.NoError(t, os.WriteFile(corrupted, full, 0o600))

	_, _, err = executeCommand(t, "restore", "--data-dir", target, "--input", corrupted)
	require.Error(t, err)
	require.ErrorIs(t, err, errDumpCorrupt)
}

func TestRestoreRejectsForeignStream(t *testing.T) {
	target := filepath.Join(t.TempDir(), "db")

	_, _, err := executeCommandWithInput(t, "definitely not a dump", "restore", "--data-dir", target)
	require.Error(t, err)
	require.ErrorIs(t, err, errDumpCorrupt)
}

func TestDumpRejectsAddrTogetherWithDataDir(t *testing.T) {
	_, _, err := executeCommand(t, "dump", "--data-dir", t.TempDir(), "--addr", "127.0.0.1:1")
	require.Error(t, err)
	require.ErrorContains(t, err, "none of the others can be")
}

func keyForIndex(i int) string {
	return "key-" + pad6(i)
}

func valueForIndex(i int) string {
	return "value-" + pad6(i)
}

func pad6(i int) string {
	digits := []byte("000000")
	for pos := len(digits) - 1; pos >= 0 && i > 0; pos-- {
		digits[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(digits)
}
