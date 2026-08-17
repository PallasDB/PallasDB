package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestUnknownConfigKeyIsRejected(t *testing.T) {
	// `adr` is the kind of typo that used to be silently ignored, leaving the
	// default address in place.
	configFile := writeConfigFile(t, "serve:\n  grpc:\n    adr: \":6000\"\n")

	stdout, stderr, err := executeCommand(t, "--config", configFile, "version")
	require.Error(t, err)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	require.ErrorContains(t, err, "invalid configuration")
	require.ErrorContains(t, err, "adr")
}

func TestUnknownTopLevelConfigSectionIsRejected(t *testing.T) {
	configFile := writeConfigFile(t, "storage:\n  compaction: aggressive\n")

	_, _, err := executeCommand(t, "--config", configFile, "version")
	require.Error(t, err)
	require.ErrorContains(t, err, "storage")
}

func TestBadAddressInConfigIsRejected(t *testing.T) {
	configFile := writeConfigFile(t, "serve:\n  grpc:\n    addr: \"not-an-address\"\n")

	_, _, err := executeCommand(t, "--config", configFile, "version")
	require.Error(t, err)
	require.ErrorContains(t, err, "serve.grpc.addr")
	require.ErrorContains(t, err, "not-an-address")
}

func TestBadPortInConfigIsRejected(t *testing.T) {
	configFile := writeConfigFile(t, "cluster:\n  raft_addr: \"127.0.0.1:99999\"\n")

	_, _, err := executeCommand(t, "--config", configFile, "version")
	require.Error(t, err)
	require.ErrorContains(t, err, "cluster.raft_addr")
}

func TestBadLogFormatIsRejectedOnLocalCommands(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "db")

	// `local *` never builds a logger, so this used to succeed silently.
	stdout, stderr, err := executeCommand(t,
		"local", "put", "alpha", "bravo",
		"--data-dir", dataDir,
		"--log-format", "bogus",
	)
	require.Error(t, err)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	require.ErrorContains(t, err, "unsupported log format")

	// Nothing was written: the command never ran.
	_, err = os.Stat(dataDir)
	require.True(t, os.IsNotExist(err))
}

func TestBadLogFormatIsRejectedFromConfigFile(t *testing.T) {
	configFile := writeConfigFile(t, "log:\n  format: bogus\n")

	_, _, err := executeCommand(t, "--config", configFile, "version")
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported log format")
}

func TestCacheSizingIsValidated(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name:    "counters exceed budget",
			content: "cache:\n  enabled: true\n  max_cost_bytes: 1000\n  num_counters: 1000000\n",
			wantErr: "cache.num_counters",
		},
		{
			name:    "zero budget",
			content: "cache:\n  enabled: true\n  max_cost_bytes: 0\n  num_counters: 10\n",
			wantErr: "cache.max_cost_bytes must be positive",
		},
		{
			name:    "disabled cache is not validated",
			content: "cache:\n  enabled: false\n  max_cost_bytes: 0\n  num_counters: 0\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configFile := writeConfigFile(t, tt.content)
			_, _, err := executeCommand(t, "--config", configFile, "version")
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestClusterStartRequiresNodeID(t *testing.T) {
	require.NoError(t, defaultConfigForTest(t).validate("pallasdb version"))

	cfg := defaultConfigForTest(t)
	err := cfg.validate("pallasdb cluster start")
	require.Error(t, err)
	require.ErrorContains(t, err, "cluster.node_id")

	cfg.Cluster.NodeID = "node-1"
	require.NoError(t, cfg.validate("pallasdb cluster start"))
}

func TestExampleConfigFileIsValid(t *testing.T) {
	// The shipped example must survive the strict decoder, or it documents a
	// configuration the binary refuses to start with.
	_, _, err := executeCommand(t, "--config", "../../pallasdb.example.yaml", "version")
	require.NoError(t, err)
}

func TestEnvValueForNonStringKeyIsDecoded(t *testing.T) {
	t.Setenv("PALLASDB_CACHE_ENABLED", "true")
	t.Setenv("PALLASDB_CACHE_MAX_COST_BYTES", "2048")
	t.Setenv("PALLASDB_CACHE_NUM_COUNTERS", "64")

	opts := newConfigOptions()
	require.NoError(t, opts.load(nil))
	require.True(t, opts.viper.GetBool("cache.enabled"))
	require.Equal(t, int64(2048), opts.viper.GetInt64("cache.max_cost_bytes"))
}

func TestApplyHooksRunOnlyForTheExecutedCommandChain(t *testing.T) {
	config := newConfigOptions()
	var ran []string

	root := &cobra.Command{Use: "root"}
	config.registerApply(func(*viper.Viper) { ran = append(ran, "root") })
	config.scope(root)

	alpha := &cobra.Command{Use: "alpha"}
	config.registerApply(func(*viper.Viper) { ran = append(ran, "alpha") })
	root.AddCommand(config.scope(alpha))

	bravo := &cobra.Command{Use: "bravo"}
	config.registerApply(func(*viper.Viper) { ran = append(ran, "bravo") })
	root.AddCommand(config.scope(bravo))

	config.apply(alpha)
	require.Equal(t, []string{"root", "alpha"}, ran, "bravo's hook must not run")

	ran = nil
	config.apply(bravo)
	require.Equal(t, []string{"root", "bravo"}, ran)
}

func TestValidateHostPort(t *testing.T) {
	require.NoError(t, validateHostPort("f", ":50051", false))
	require.NoError(t, validateHostPort("f", "127.0.0.1:0", false))
	require.NoError(t, validateHostPort("f", "example.internal:7946", false))
	require.NoError(t, validateHostPort("f", "", true))
	require.Error(t, validateHostPort("f", "", false))
	require.Error(t, validateHostPort("f", "localhost", false))
	require.Error(t, validateHostPort("f", "localhost:http", false))
	require.Error(t, validateHostPort("f", "localhost:-1", false))
	require.Error(t, validateHostPort("f", "bad host:1", false))
}

// defaultConfigForTest returns the fully defaulted configuration, which is what
// a run with no config file and no flags resolves to.
func defaultConfigForTest(t *testing.T) *Config {
	t.Helper()

	opts := newConfigOptions()
	cfg := &Config{}
	require.NoError(t, opts.viper.Unmarshal(cfg, decoderOptions...))
	return cfg
}
