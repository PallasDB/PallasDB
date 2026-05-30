package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfigFileProvidesLocalDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "db")
	configFile := writeConfigFile(t, "local:\n  data_dir: "+quoteYAML(dataDir)+"\n")

	stdout, stderr, err := executeCommand(t, "--config", configFile, "local", "put", "alpha", "bravo")
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "updated\n", stdout)

	stdout, stderr, err = executeCommand(t, "--config", configFile, "local", "get", "alpha")
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "bravo\n", stdout)
}

func TestEnvOverridesConfigFile(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config-db")
	envDir := filepath.Join(t.TempDir(), "env-db")
	configFile := writeConfigFile(t, "local:\n  data_dir: "+quoteYAML(configDir)+"\n")
	t.Setenv("PALLASDB_LOCAL_DATA_DIR", envDir)

	_, _, err := executeCommand(t, "--config", configFile, "local", "put", "alpha", "bravo")
	require.NoError(t, err)

	stdout, stderr, err := executeCommand(t, "local", "get", "alpha", "--data-dir", envDir)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "bravo\n", stdout)

	stdout, stderr, err = executeCommand(t, "local", "get", "alpha", "--data-dir", configDir)
	require.Error(t, err)
	require.Empty(t, stderr)
	require.Empty(t, stdout)
}

func TestFlagOverridesEnv(t *testing.T) {
	envDir := filepath.Join(t.TempDir(), "env-db")
	flagDir := filepath.Join(t.TempDir(), "flag-db")
	t.Setenv("PALLASDB_LOCAL_DATA_DIR", envDir)

	_, _, err := executeCommand(t, "local", "put", "alpha", "bravo", "--data-dir", flagDir)
	require.NoError(t, err)

	stdout, stderr, err := executeCommand(t, "local", "get", "alpha", "--data-dir", flagDir)
	require.NoError(t, err)
	require.Empty(t, stderr)
	require.Equal(t, "bravo\n", stdout)

	stdout, stderr, err = executeCommand(t, "local", "get", "alpha", "--data-dir", envDir)
	require.Error(t, err)
	require.Empty(t, stderr)
	require.Empty(t, stdout)
}

func TestExplicitMissingConfigFileReturnsError(t *testing.T) {
	missingConfig := filepath.Join(t.TempDir(), "missing.yaml")

	stdout, stderr, err := executeCommand(t, "--config", missingConfig, "version")
	require.Error(t, err)
	require.Empty(t, stderr)
	require.Empty(t, stdout)
	require.Contains(t, err.Error(), "read config")
}

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()

	configFile := filepath.Join(t.TempDir(), "pallasdb.yaml")
	require.NoError(t, writeFile(configFile, content))
	return configFile
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func quoteYAML(value string) string {
	return strconv.Quote(value)
}
