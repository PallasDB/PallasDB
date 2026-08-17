package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// linearizableReadTimeout is deliberately far shorter than the 10s apply
// timeout a real request gets: the point of these tests is that a linearizable
// read completes promptly, not merely that it completes eventually.
const linearizableReadTimeout = 3 * time.Second

// awaitCommitIndex is the wait a linearizable read performs
// (grpc.ClusterKVServer.readReady) once leadership is confirmed.
func awaitCommitIndex(t *testing.T, node *harnessNode) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), linearizableReadTimeout)
	defer cancel()
	return node.FSM().WaitForAppliedIndex(ctx, node.Raft().CommitIndex())
}

// A newly elected leader appends a no-op entry that raft never routes to an
// FSM, so the commit index moves without the applied index following. Without
// the leadership fence a linearizable read on an idle cluster waits for that
// index forever — until some unrelated write happens to close the gap.
func TestLinearizableReadAfterElectionWithoutWrites(t *testing.T) {
	h := harnessNew(t)
	leader := h.start("n1", func(c *Config) { c.Bootstrap = true })
	h.waitLeader()

	require.NoError(t, awaitCommitIndex(t, leader),
		"linearizable read stalled on an idle cluster that has never been written to")
}

// The same gap reopens on every election, so a failover must not leave the new
// leader unable to serve linearizable reads until the next write.
func TestLinearizableReadAfterFailover(t *testing.T) {
	h := harnessNew(t)
	h.start("n1", func(c *Config) { c.Bootstrap = true })
	h.start("n2", nil)
	h.start("n3", nil)
	h.waitVoters("n1", "n2", "n3")

	old := h.waitLeader()
	harnessPut(t, old, "k", "v")
	require.NoError(t, h.stop(old.id))

	next := h.waitLeader()
	require.NotEqual(t, old.id, next.id)

	require.NoError(t, awaitCommitIndex(t, next),
		"linearizable read stalled on the newly elected leader")
}

// The fence must not report an applied index the FSM has not reached. Raft's
// own AppliedIndex() runs ahead of the FSM (processLogs sets it right after the
// non-blocking hand-off to fsmMutateCh), so the fence uses a barrier instead:
// every key committed before the fence must be readable the moment it returns.
func TestFenceAppliedIndexImpliesReadableData(t *testing.T) {
	h := harnessNew(t)
	leader := h.start("n1", func(c *Config) { c.Bootstrap = true })
	h.waitLeader()

	for i := range 64 {
		harnessPut(t, leader, "k"+string(rune('a'+i%26))+string(rune('a'+i/26)), "v")
	}

	require.NoError(t, awaitCommitIndex(t, leader))

	// Everything the applied index covers must be present in the store.
	val, ok, err := leader.FSM().Get([]byte("kaa"))
	require.NoError(t, err)
	require.True(t, ok, "applied index advanced past a write that is not readable")
	require.Equal(t, "v", string(val))
}
