package main

import (
	"crypto/tls"
	"encoding/base64"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"github.com/teddymalhan/pallasdb/db"
)

func applyKVOptions(t *testing.T, opts []db.KVOption) db.KVOptions {
	t.Helper()
	var built db.KVOptions
	for _, opt := range opts {
		require.NoError(t, opt(&built))
	}
	return built
}

// A cluster node applies every committed Raft entry to the KV write-ahead log,
// so compaction must be on by default or the data directory grows forever.
func TestClusterKVOptionsCompactByDefault(t *testing.T) {
	opts := &clusterStartOptions{autoCompact: true}

	built := applyKVOptions(t, clusterKVOptions(opts, viper.New()))

	require.True(t, built.AutoCompact)
}

func TestClusterKVOptionsRespectDisabledCompaction(t *testing.T) {
	opts := &clusterStartOptions{autoCompact: false}

	built := applyKVOptions(t, clusterKVOptions(opts, viper.New()))

	require.False(t, built.AutoCompact)
}

func TestClusterKVOptionsKeepCacheSettings(t *testing.T) {
	v := viper.New()
	v.Set("cache.enabled", true)
	v.Set("cache.max_cost_bytes", int64(1024))
	v.Set("cache.num_counters", int64(64))

	built := applyKVOptions(t, clusterKVOptions(&clusterStartOptions{autoCompact: true}, v))

	require.True(t, built.AutoCompact)
	require.True(t, built.CacheEnabled)
	require.Equal(t, int64(1024), built.CacheMaxCost)
}

func TestClusterBootstrapExpectSuppressesSelfBootstrap(t *testing.T) {
	tests := []struct {
		name          string
		opts          clusterStartOptions
		wantBootstrap bool
		wantExpect    int
	}{
		{
			name:          "single node dev startup still bootstraps immediately",
			opts:          clusterStartOptions{serfEnabled: true, bootstrapExpect: 1},
			wantBootstrap: true,
			wantExpect:    0,
		},
		{
			name:          "bootstrap-expect defers to the coordinated bootstrap",
			opts:          clusterStartOptions{serfEnabled: true, bootstrapExpect: 3},
			wantBootstrap: false,
			wantExpect:    3,
		},
		{
			name:          "join wins over bootstrap-expect",
			opts:          clusterStartOptions{serfEnabled: true, bootstrapExpect: 3, joinAddr: "localhost:50051"},
			wantBootstrap: false,
			wantExpect:    0,
		},
		{
			name:          "non-voter never bootstraps",
			opts:          clusterStartOptions{serfEnabled: true, bootstrapExpect: 1, nonVoter: true},
			wantBootstrap: false,
			wantExpect:    0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.wantBootstrap, shouldBootstrapCluster(&tt.opts))
			require.Equal(t, tt.wantExpect, bootstrapExpectCount(&tt.opts))
		})
	}
}

func TestClusterValidateBootstrapExpect(t *testing.T) {
	tests := []struct {
		name    string
		opts    clusterStartOptions
		wantErr string
	}{
		{
			name: "default is valid",
			opts: clusterStartOptions{serfEnabled: true, bootstrapExpect: 1},
		},
		{
			name:    "zero is rejected",
			opts:    clusterStartOptions{serfEnabled: true, bootstrapExpect: 0},
			wantErr: "--bootstrap-expect must be at least 1",
		},
		{
			name:    "needs gossip to count members",
			opts:    clusterStartOptions{serfEnabled: false, bootstrapExpect: 3},
			wantErr: "--bootstrap-expect requires --serf-enabled",
		},
		{
			name:    "conflicts with an explicit join",
			opts:    clusterStartOptions{serfEnabled: true, bootstrapExpect: 3, joinAddr: "localhost:50051"},
			wantErr: "--bootstrap-expect and --join are mutually exclusive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBootstrapExpect(&tt.opts)
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestClusterDecodeSerfEncryptKey(t *testing.T) {
	empty, err := decodeSerfEncryptKey("")
	require.NoError(t, err)
	require.Nil(t, empty)

	key := make([]byte, 32)
	decoded, err := decodeSerfEncryptKey(base64.StdEncoding.EncodeToString(key))
	require.NoError(t, err)
	require.Len(t, decoded, 32)

	_, err = decodeSerfEncryptKey("!!!not base64!!!")
	require.ErrorContains(t, err, "must be base64")

	_, err = decodeSerfEncryptKey(base64.StdEncoding.EncodeToString(make([]byte, 20)))
	require.ErrorContains(t, err, "16, 24, or 32 bytes")
}

func TestClusterTLSConfigFromOptions(t *testing.T) {
	plaintext, err := clusterTLSConfig(&clusterStartOptions{tlsMinVersion: "1.2"})
	require.NoError(t, err)
	require.Nil(t, plaintext)

	_, err = clusterTLSConfig(&clusterStartOptions{tlsRequireClientCert: true})
	require.ErrorContains(t, err, "--tls-require-client-cert needs")

	cfg, err := clusterTLSConfig(&clusterStartOptions{
		tlsCertFile:   "cert.pem",
		tlsKeyFile:    "key.pem",
		tlsMinVersion: "1.3",
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)

	_, err = clusterTLSConfig(&clusterStartOptions{tlsCertFile: "cert.pem", tlsMinVersion: "1.9"})
	require.ErrorContains(t, err, "--tls-min-version must be 1.2 or 1.3")
}
