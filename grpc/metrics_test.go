package grpcapi

import (
	"testing"

	gometrics "github.com/armon/go-metrics"
	"github.com/stretchr/testify/require"
)

// Raft emits its replication counters through go-metrics; nothing collected
// them before, so they never reached a scrape endpoint.
func TestCollectRaftMetrics(t *testing.T) {
	metrics := NewMetrics()
	require.NoError(t, metrics.CollectRaftMetrics("pallasdb"))

	gometrics.IncrCounter([]string{"raft", "apply"}, 1)

	families, err := metrics.Registry().Gather()
	require.NoError(t, err)

	names := make([]string, 0, len(families))
	for _, family := range families {
		names = append(names, family.GetName())
	}
	require.Contains(t, names, "pallasdb_raft_apply")
}

func TestSplitMethod(t *testing.T) {
	service, method := splitMethod("/pallasdb.v1.KVService/Get")
	require.Equal(t, "pallasdb.v1.KVService", service)
	require.Equal(t, "Get", method)

	service, method = splitMethod("malformed")
	require.Equal(t, "malformed", service)
	require.Empty(t, method)
}
