package discovery

import (
	"testing"

	"github.com/hashicorp/serf/serf"
	"github.com/stretchr/testify/require"
)

func TestNewSerfDiscoveryValidatesConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "missing node id",
			cfg: Config{
				RaftAddr: ":7001",
				SerfAddr: ":7946",
			},
			wantErr: "node id is required",
		},
		{
			name: "missing raft addr",
			cfg: Config{
				NodeID:   "node-1",
				SerfAddr: ":7946",
			},
			wantErr: "raft addr is required",
		},
		{
			name: "missing serf addr",
			cfg: Config{
				NodeID:   "node-1",
				RaftAddr: ":7001",
			},
			wantErr: "serf addr is required",
		},
		{
			name: "valid",
			cfg: Config{
				NodeID:   "node-1",
				RaftAddr: ":7001",
				SerfAddr: ":7946",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disc, err := NewSerfDiscovery(tt.cfg)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Nil(t, disc)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, disc)
		})
	}
}

func TestNodeInfoFromMember(t *testing.T) {
	member := serf.Member{
		Name: "node-1",
		Tags: map[string]string{
			"service":   "pallasdb",
			"node_id":   "node-1",
			"grpc_addr": ":50051",
			"raft_addr": ":7001",
		},
		Status: serf.StatusAlive,
	}

	info, ok := nodeInfoFromMember(member)

	require.True(t, ok)
	require.Equal(t, "node-1", info.NodeID)
	require.Equal(t, ":50051", info.GRPCAddr)
	require.Equal(t, ":7001", info.RaftAddr)
	require.Equal(t, MemberStatusAlive, info.Status)
}

func TestSplitHostPort(t *testing.T) {
	host, port, err := splitHostPort(":7946")
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0", host)
	require.Equal(t, 7946, port)

	host, port, err = splitHostPort("127.0.0.1:7947")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", host)
	require.Equal(t, 7947, port)

	_, _, err = splitHostPort("not-a-host-port")
	require.Error(t, err)
}
