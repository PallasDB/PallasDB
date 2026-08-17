package cluster

import (
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"
)

// startThreeNodeCluster brings up a bootstrapped leader plus two nodes that
// discover it through gossip, and waits until all three are voters.
func startThreeNodeCluster(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	h := harnessNew(t)
	h.start("n1", func(c *Config) {
		c.Bootstrap = true
		if mutate != nil {
			mutate(c)
		}
	})
	h.start("n2", mutate)
	h.start("n3", mutate)
	h.waitVoters("n1", "n2", "n3")
	h.waitLeader()
	return h
}

func TestClusterElectsLeaderAndReplicatesWrite(t *testing.T) {
	h := startThreeNodeCluster(t, nil)
	leader := h.waitLeader()

	harnessPut(t, leader, "alpha", "one")
	harnessRequireValue(t, "alpha", "one", h.node("n1"), h.node("n2"), h.node("n3"))

	// Every node must agree on who leads; a second leader means split brain.
	_, leaderID := leader.LeaderWithID()
	require.Equal(t, leader.id, leaderID)
	for _, node := range h.live() {
		harnessWaitFor(t, "agreement on the leader", func() bool {
			_, id := node.LeaderWithID()
			return id == leader.id
		})
	}
}

// A node that gossips a graceful leave must disappear from the Raft
// configuration. Before this was handled, a rolling replace left every departed
// node as a voter until the cluster could no longer reach quorum.
func TestGracefulLeaveRemovesNodeFromConfiguration(t *testing.T) {
	h := startThreeNodeCluster(t, nil)
	leader := h.waitLeader()

	victim := harnessFollower(t, h, leader)
	require.NoError(t, h.stop(victim.id))
	h.bus.leave(victim.id)

	remaining := harnessOtherIDs(h, victim.id)
	h.waitVoters(remaining...)

	servers, err := leader.Configuration()
	require.NoError(t, err)
	_, found := findServer(servers, victim.id)
	require.False(t, found, "departed node must not stay in the configuration")
	require.Len(t, servers, 2)
}

// Rolling three replacements through a three-node cluster must leave three
// voters, not seven. This is the quorum-loss scenario from the audit.
func TestRollingReplacementKeepsVoterCountStable(t *testing.T) {
	h := startThreeNodeCluster(t, nil)

	for _, generation := range []string{"n4", "n5", "n6"} {
		leader := h.waitLeader()
		victim := harnessFollower(t, h, leader)
		survivors := harnessOtherIDs(h, victim.id)

		require.NoError(t, h.stop(victim.id))
		h.bus.leave(victim.id)
		h.waitVoters(survivors...)

		h.start(generation, nil)
		h.waitVoters(append(survivors, generation)...)
	}

	leader := h.waitLeader()
	servers, err := leader.Configuration()
	require.NoError(t, err)
	require.Len(t, servers, 3, "replacements must not accumulate stale voters")
	require.Equal(t, 3, countVoters(servers))
}

// A suspected failure is not proof of death: a flapping node must survive the
// grace period, and only a node still missing when it expires is evicted.
func TestFailedNodeEvictedOnlyAfterGracePeriod(t *testing.T) {
	h := startThreeNodeCluster(t, nil)
	leader := h.waitLeader()
	victim := harnessFollower(t, h, leader)
	survivors := harnessOtherIDs(h, victim.id)

	require.NoError(t, h.stop(victim.id))
	h.bus.fail(victim.id)

	// The leader arms the grace timer but must not touch the configuration.
	harnessWaitFor(t, "the leader to arm the grace timer", func() bool {
		return leader.timers.armed() == 1
	})
	servers, err := leader.Configuration()
	require.NoError(t, err)
	require.Len(t, servers, 3, "a failed node must not be evicted before the grace period")

	// The node comes back inside the window: the eviction is cancelled.
	h.bus.revive(victim.id)
	harnessWaitFor(t, "the grace timer to be cancelled", func() bool {
		return leader.timers.armed() == 0
	})
	require.Equal(t, 0, leader.timers.elapse(), "a flapping node must never be evicted")
	servers, err = leader.Configuration()
	require.NoError(t, err)
	require.Len(t, servers, 3)

	// It fails again and stays gone: this time the grace period expires.
	h.bus.fail(victim.id)
	harnessWaitFor(t, "the leader to re-arm the grace timer", func() bool {
		return leader.timers.armed() == 1
	})
	require.Equal(t, 1, leader.timers.elapse())

	h.waitVoters(survivors...)
	servers, err = leader.Configuration()
	require.NoError(t, err)
	_, found := findServer(servers, victim.id)
	require.False(t, found, "a node still failed after the grace period must be evicted")
}

// A replica that is still receiving the log must not count toward quorum, so it
// enters as a non-voter and is promoted only once it has caught up.
func TestNonVoterJoinPromotedOnceCaughtUp(t *testing.T) {
	h := harnessNew(t)
	leader := h.start("n1", func(c *Config) { c.Bootstrap = true })
	h.waitLeader()

	replica := h.start("n2", func(c *Config) { c.NonVoter = true })

	harnessWaitFor(t, "the replica to be added as a non-voter", func() bool {
		suffrage, found := harnessSuffrage(t, leader.Node, "n2")
		return found && suffrage == raft.Nonvoter
	})
	require.True(t, harnessSameVoters(leader.Node, []string{"n1"}),
		"a catching-up replica must not change the voter set")

	// Quorum is still one node, so writes keep committing, and the replica
	// receives them even though it cannot vote.
	harnessPut(t, leader, "beta", "two")
	harnessRequireValue(t, "beta", "two", leader, replica)

	// A permanent read replica never asks for promotion on its own.
	require.Never(t, func() bool {
		suffrage, found := harnessSuffrage(t, leader.Node, "n2")
		return found && suffrage == raft.Voter
	}, 200*time.Millisecond, 20*time.Millisecond)

	harnessWaitFor(t, "the replica to catch up", replica.caughtUp)
	require.NoError(t, leader.Join("n2", string(replica.raftAddr), replica.grpcAddr, false))
	h.waitVoters("n1", "n2")
}

// Join must never put an unknown node straight into the voting set, whatever
// the request asks for.
func TestJoinAddsUnknownNodeAsNonVoterEvenWhenVoterRequested(t *testing.T) {
	h := harnessNew(t)
	leader := h.start("n1", func(c *Config) { c.Bootstrap = true })
	h.waitLeader()

	require.NoError(t, leader.Join("phantom", "phantom-addr", "grpc://phantom", false))

	suffrage, found := harnessSuffrage(t, leader.Node, "phantom")
	require.True(t, found)
	require.Equal(t, raft.Nonvoter, suffrage)
	require.True(t, harnessSameVoters(leader.Node, []string{"n1"}))

	// A second call, now that it is a known non-voter, is the promotion step.
	require.NoError(t, leader.Join("phantom", "phantom-addr", "grpc://phantom", false))
	suffrage, found = harnessSuffrage(t, leader.Node, "phantom")
	require.True(t, found)
	require.Equal(t, raft.Voter, suffrage)
}

// A symmetric start of three identically configured nodes must produce one
// cluster. Without bootstrap-expect each node bootstrapped itself, giving three
// independent single-node clusters that all accepted writes.
func TestBootstrapExpectFormsSingleCluster(t *testing.T) {
	h := harnessNew(t)
	expect := func(c *Config) { c.BootstrapExpect = 3 }
	// Started out of node-ID order to prove the tiebreak is not "first wins".
	h.start("n3", expect)
	h.start("n1", expect)
	h.start("n2", expect)

	h.waitVoters("n1", "n2", "n3")
	leader := h.waitLeader()

	// One configuration, one leader, one log: all three must agree.
	for _, node := range h.live() {
		servers, err := node.Configuration()
		require.NoError(t, err)
		require.Len(t, servers, 3, "%s sees a different cluster", node.id)
		harnessWaitFor(t, "agreement on the leader", func() bool {
			_, id := node.LeaderWithID()
			return id == leader.id
		})
	}

	harnessPut(t, leader, "gamma", "three")
	harnessRequireValue(t, "gamma", "three", h.node("n1"), h.node("n2"), h.node("n3"))
}

// Shutting the leader down hands leadership over first, so the cluster keeps a
// leader and keeps accepting writes.
func TestLeaderShutdownTransfersLeadership(t *testing.T) {
	h := startThreeNodeCluster(t, nil)
	oldLeader := h.waitLeader()

	require.NoError(t, h.stop(oldLeader.id))

	newLeader := h.waitLeader()
	require.NotEqual(t, oldLeader.id, newLeader.id)

	harnessPut(t, newLeader, "delta", "four")
	survivors := h.live()
	require.Len(t, survivors, 2)
	harnessRequireValue(t, "delta", "four", survivors...)

	// Without LeaveOnShutdown the departed node stays in the configuration; it
	// is expected back after a restart.
	servers, err := newLeader.Configuration()
	require.NoError(t, err)
	_, found := findServer(servers, oldLeader.id)
	require.True(t, found)
}

// With LeaveOnShutdown the leader transfers leadership and then removes itself,
// leaving no dead voter behind.
func TestShutdownWithLeaveRemovesSelfFromConfiguration(t *testing.T) {
	h := startThreeNodeCluster(t, func(c *Config) { c.LeaveOnShutdown = true })
	oldLeader := h.waitLeader()

	require.NoError(t, h.stop(oldLeader.id))

	h.waitVoters(harnessOtherIDs(h, oldLeader.id)...)
	newLeader := h.waitLeader()
	servers, err := newLeader.Configuration()
	require.NoError(t, err)
	require.Len(t, servers, 2)
	_, found := findServer(servers, oldLeader.id)
	require.False(t, found, "a node that left must not remain in the configuration")

	harnessPut(t, newLeader, "epsilon", "five")
	harnessRequireValue(t, "epsilon", "five", h.live()...)
}

// A single node cannot remove itself: an empty configuration can never elect
// anyone again.
func TestLeaveSelfKeepsLastServer(t *testing.T) {
	h := harnessNew(t)
	only := h.start("n1", func(c *Config) { c.Bootstrap = true })
	h.waitLeader()

	require.NoError(t, only.LeaveSelf())

	servers, err := only.Configuration()
	require.NoError(t, err)
	require.Len(t, servers, 1)
}

// harnessFollower returns a live node that is not the leader.
func harnessFollower(t *testing.T, h *harness, leader *harnessNode) *harnessNode {
	t.Helper()
	for _, node := range h.live() {
		if node.id != leader.id {
			return node
		}
	}
	t.Fatalf("no follower besides %s", leader.id)
	return nil
}

// harnessOtherIDs lists every live node except the named one.
func harnessOtherIDs(h *harness, exclude string) []string {
	ids := make([]string, 0, 4)
	for _, node := range h.live() {
		if node.id != exclude {
			ids = append(ids, node.id)
		}
	}
	return ids
}
