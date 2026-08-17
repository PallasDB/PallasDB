package discovery

import (
	"bytes"
	"testing"
	"time"

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

func TestValidateEncryptKey(t *testing.T) {
	for _, size := range []int{0, 16, 24, 32} {
		require.NoError(t, validateEncryptKey(make([]byte, size)))
	}
	for _, size := range []int{1, 15, 31, 33} {
		require.ErrorContains(t, validateEncryptKey(make([]byte, size)), "16, 24, or 32 bytes")
	}
}

func TestNewSerfDiscoveryRejectsBadEncryptKey(t *testing.T) {
	_, err := NewSerfDiscovery(Config{
		NodeID:     "node-1",
		RaftAddr:   ":7001",
		SerfAddr:   ":7946",
		EncryptKey: make([]byte, 7),
	})
	require.ErrorContains(t, err, "16, 24, or 32 bytes")
}

// serftestStart brings up a Serf node on an ephemeral loopback port.
func serftestStart(t *testing.T, nodeID string, key []byte, joinAddrs ...string) *SerfDiscovery {
	t.Helper()
	disc, err := NewSerfDiscovery(Config{
		NodeID:     nodeID,
		GRPCAddr:   "127.0.0.1:0",
		RaftAddr:   "127.0.0.1:0",
		SerfAddr:   "127.0.0.1:0",
		JoinAddrs:  joinAddrs,
		EncryptKey: key,
	})
	require.NoError(t, err)
	if err := disc.Start(); err != nil {
		return nil
	}
	t.Cleanup(func() { _ = disc.Shutdown() })
	return disc
}

// serftestSelfAddr reports the gossip address a peer should dial.
func serftestSelfAddr(t *testing.T, disc *SerfDiscovery, nodeID string) string {
	t.Helper()
	for _, member := range disc.Members() {
		if member.NodeID == nodeID {
			return member.SerfAddr
		}
	}
	t.Fatalf("%s does not know its own address", nodeID)
	return ""
}

// Two nodes sharing a gossip key must find each other, and a node with the
// wrong key must not be able to join: without SecretKey wiring both would work.
func TestSerfDiscoveryEncryptedGossip(t *testing.T) {
	key := bytes.Repeat([]byte{0x2a}, 32)

	first := serftestStart(t, "node-1", key)
	require.NotNil(t, first, "the seed node must start")
	seed := serftestSelfAddr(t, first, "node-1")

	second := serftestStart(t, "node-2", key, seed)
	require.NotNil(t, second, "a node with the right key must join")

	require.Eventually(t, func() bool {
		return len(first.Members()) == 2 && len(second.Members()) == 2
	}, 10*time.Second, 20*time.Millisecond, "encrypted gossip should converge")

	wrongKey := bytes.Repeat([]byte{0x3b}, 32)
	require.Nil(t, serftestStart(t, "node-3", wrongKey, seed),
		"a node with the wrong gossip key must not be able to join")

	require.Never(t, func() bool {
		return len(first.Members()) > 2
	}, 300*time.Millisecond, 50*time.Millisecond)
}
