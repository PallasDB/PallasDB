package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalPutGetDelete(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "db")

	stdout, stderr, err := executeCommand(t, "local", "put", "alpha", "bravo", "--data-dir", dataDir)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "updated\n", stdout)

	stdout, stderr, err = executeCommand(t, "local", "get", "alpha", "--data-dir", dataDir)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "bravo\n", stdout)

	stdout, stderr, err = executeCommand(t, "local", "delete", "alpha", "--data-dir", dataDir)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "deleted\n", stdout)

	stdout, stderr, err = executeCommand(t, "local", "get", "alpha", "--data-dir", dataDir)
	require.Error(t, err)
	require.Empty(t, stderr)
	require.Empty(t, stdout)
}

func TestLocalRange(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "db")

	_, _, err := executeCommand(t, "local", "put", "a", "1", "--data-dir", dataDir)
	require.NoError(t, err)
	_, _, err = executeCommand(t, "local", "put", "b", "2", "--data-dir", dataDir)
	require.NoError(t, err)
	_, _, err = executeCommand(t, "local", "put", "c", "3", "--data-dir", dataDir)
	require.NoError(t, err)

	stdout, stderr, err := executeCommand(t, "local", "range", "a", "c", "--data-dir", dataDir)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "a\t1\nb\t2\nc\t3\n", stdout)
}

func TestLocalBenchmarkValidation(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "db")

	stdout, stderr, err := executeCommand(t,
		"local", "benchmark",
		"--data-dir", dataDir,
		"--keys", "0",
	)

	require.Error(t, err)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	require.ErrorContains(t, err, "keys must be positive")
}

func TestLocalBenchmarkSmoke(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "db")

	stdout, stderr, err := executeCommand(t,
		"local", "benchmark",
		"--data-dir", dataDir,
		"--reset",
		"--keys", "100",
		"--value-size", "32",
		"--batch-size", "25",
		"--read-ops", "80",
		"--scan-limit", "40",
		"--compact",
		"--format", "text",
	)

	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Contains(t, stdout, "PallasDB local benchmark")
	require.Contains(t, stdout, "environment:")
	require.Contains(t, stdout, "  populate:")
	require.Contains(t, stdout, "    operations: 100")
	require.Contains(t, stdout, "  random_read:")
	require.Contains(t, stdout, "    found: 80")
	require.Contains(t, stdout, "  iterate_keys:")
	require.Contains(t, stdout, "    operations: 40")
	require.Contains(t, stdout, "  iterate_values:")
	require.Contains(t, stdout, "data_dir_size_bytes:")
}

func TestLocalBenchmarkOutputFile(t *testing.T) {
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "db")
	output := filepath.Join(dir, "benchmark.txt")

	stdout, stderr, err := executeCommand(t,
		"local", "benchmark",
		"--data-dir", dataDir,
		"--reset",
		"--keys", "20",
		"--value-size", "16",
		"--batch-size", "5",
		"--read-ops", "10",
		"--scan-limit", "10",
		"--format", "text",
		"--output", output,
	)

	require.NoError(t, err)
	require.Empty(t, stderr)
	written, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, stdout, string(written))
	require.Contains(t, string(written), "  random_read:")
}
