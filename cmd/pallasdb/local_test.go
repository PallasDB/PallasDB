package main

import (
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
