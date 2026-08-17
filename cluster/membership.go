package cluster

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/cluster/discovery"
)

// ErrNotLeader is returned by membership operations that only the Raft leader
// may perform. It wraps raft.ErrNotLeader so callers that already map raft's
// sentinels to RPC statuses get the right answer without a special case.
var ErrNotLeader = fmt.Errorf("cluster: node is not the raft leader: %w", raft.ErrNotLeader)

// ErrNoLeader is returned when an operation needs the current leader but no
// leader is known yet.
var ErrNoLeader = errors.New("cluster: no known raft leader")

// Discovery is the membership-gossip surface a Node depends on. The Serf
// adapter in cluster/discovery implements it.
type Discovery interface {
	Events() <-chan discovery.Event
	Members() []discovery.NodeInfo
	// Leave announces an intentional departure to the other members.
	Leave() error
	Shutdown() error
}

// JoinRequest describes a node asking to enter the Raft configuration.
type JoinRequest struct {
	NodeID   string
	RaftAddr string
	GRPCAddr string
	NonVoter bool
}

// JoinFunc delivers a JoinRequest to the cluster leader's gRPC endpoint.
type JoinFunc func(ctx context.Context, leaderGRPCAddr string, req JoinRequest) error

// LeaveFunc asks the leader to remove nodeID from the configuration.
type LeaveFunc func(ctx context.Context, leaderGRPCAddr, nodeID string) error

// stopper is the subset of *time.Timer the eviction scheduler needs. It exists
// so tests can drive the failure grace period without wall-clock sleeps.
type stopper interface{ Stop() bool }

// afterFunc schedules f to run after d and returns a handle that cancels it.
type afterFunc func(d time.Duration, f func()) stopper

func realAfterFunc(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }

// IsLeader reports whether this node currently leads the cluster.
func (n *Node) IsLeader() bool { return n.raft.State() == raft.Leader }

// LeaderWithID returns the current leader's Raft address and node ID. Both are
// empty when no leader is known.
func (n *Node) LeaderWithID() (raftAddr string, nodeID string) {
	addr, id := n.raft.LeaderWithID()
	return string(addr), string(id)
}

// GRPCAddrForID resolves a node ID to the gRPC address clients should dial.
// It consults addresses recorded from JoinRequests first, then Serf discovery.
// Returns "" when the node is unknown.
func (n *Node) GRPCAddrForID(id string) string {
	if id == "" {
		return ""
	}
	if id == n.cfg.NodeID {
		return n.cfg.GRPCAddr
	}
	n.mu.Lock()
	addr := n.grpcAddrs[id]
	n.mu.Unlock()
	if addr != "" {
		return addr
	}
	for _, member := range n.DiscoveryMembers() {
		if member.NodeID == id && member.GRPCAddr != "" {
			return member.GRPCAddr
		}
	}
	return ""
}

// LeaderGRPCAddr returns the gRPC address of the current leader, or "" when
// there is no leader or its address is not known yet.
func (n *Node) LeaderGRPCAddr() string {
	_, id := n.raft.LeaderWithID()
	return n.GRPCAddrForID(string(id))
}

// Configuration returns the servers in the latest known Raft configuration.
func (n *Node) Configuration() ([]raft.Server, error) {
	future := n.raft.GetConfiguration()
	if err := future.Error(); err != nil {
		return nil, fmt.Errorf("cluster: raft configuration: %w", err)
	}
	return future.Configuration().Servers, nil
}

// Join adds a peer to the Raft configuration. It must run on the leader.
//
// A node that is not already in the configuration always enters as a
// non-voter, whatever nonVoter says: a fresh replica must not count toward
// quorum while it is still receiving the log or a snapshot. The replica asks
// for promotion again once its applied index has caught up, and that second
// call — nonVoter=false against an existing non-voter — is the AddVoter step.
func (n *Node) Join(nodeID, raftAddr, grpcAddr string, nonVoter bool) error {
	if nodeID == "" || raftAddr == "" {
		return fmt.Errorf("cluster: join requires a node id and raft addr")
	}
	if !n.IsLeader() {
		return ErrNotLeader
	}
	if grpcAddr != "" {
		n.rememberGRPCAddr(nodeID, grpcAddr)
	}
	n.cancelPendingRemoval(nodeID)

	servers, err := n.Configuration()
	if err != nil {
		return err
	}
	existing, found := findServer(servers, nodeID)
	address := raft.ServerAddress(raftAddr)
	unmoved := found && existing.Address == address

	switch {
	case unmoved && existing.Suffrage == raft.Voter:
		return nil // already a voter; a join request never demotes
	case unmoved && nonVoter:
		return nil // already the non-voter the caller asked for
	case unmoved, found && existing.Suffrage == raft.Voter:
		// Either a caught-up non-voter asking for promotion, or a known voter
		// that moved address. Both keep or gain voting rights.
		if err := n.raft.AddVoter(raft.ServerID(nodeID), address, 0, n.cfg.Timeout).Error(); err != nil {
			return fmt.Errorf("cluster: promote %s to voter: %w", nodeID, err)
		}
	default:
		// A node the configuration does not know, or a non-voter that moved.
		if err := n.raft.AddNonvoter(raft.ServerID(nodeID), address, 0, n.cfg.Timeout).Error(); err != nil {
			return fmt.Errorf("cluster: add nonvoter %s: %w", nodeID, err)
		}
	}
	return nil
}

// Leave removes a peer from the Raft configuration. It must run on the leader
// and is idempotent: removing an unknown node succeeds.
func (n *Node) Leave(nodeID string) error {
	if nodeID == "" {
		return fmt.Errorf("cluster: leave requires a node id")
	}
	if nodeID == n.cfg.NodeID {
		return n.LeaveSelf()
	}
	if !n.IsLeader() {
		return ErrNotLeader
	}
	n.cancelPendingRemoval(nodeID)
	n.forgetGRPCAddr(nodeID)
	n.markEvicted(nodeID)
	return n.removeServer(nodeID)
}

// LeaveSelf removes this node from the Raft configuration. When this node is
// the leader it transfers leadership away first so the cluster keeps a leader
// across the removal; when it is a follower it asks the leader to evict it.
func (n *Node) LeaveSelf() error {
	if err := n.transferLeadership(); err != nil {
		return err
	}
	if n.IsLeader() {
		// Leadership could not move: we are the only voter. Removing ourselves
		// would leave an empty configuration that cannot elect anyone.
		servers, err := n.Configuration()
		if err != nil {
			return err
		}
		if countVoters(servers) <= 1 {
			return nil
		}
		return n.removeServer(n.cfg.NodeID)
	}

	// Leadership has just moved, so the new leader's identity may not have
	// reached this node yet.
	leader := n.waitForLeaderGRPCAddr(n.cfg.Timeout)
	if leader == "" {
		leader = n.cfg.JoinAddr
	}
	if leader == "" {
		return ErrNoLeader
	}
	ctx, cancel := context.WithTimeout(context.Background(), n.cfg.Timeout)
	defer cancel()
	return n.leaveRPC(ctx, leader, n.cfg.NodeID)
}

// waitForLeaderGRPCAddr polls until this node learns the leader's gRPC address
// or the timeout expires. It returns "" if the leader never becomes known.
func (n *Node) waitForLeaderGRPCAddr(timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		if addr := n.LeaderGRPCAddr(); addr != "" && addr != n.cfg.GRPCAddr {
			return addr
		}
		if n.IsLeader() || time.Now().After(deadline) {
			return ""
		}
		// Deliberately wall-clock only: the node context is already cancelled
		// by the time a shutdown reaches here.
		time.Sleep(10 * time.Millisecond)
	}
}

// removeServer drops nodeID from the configuration, refusing to leave one that
// cannot elect a leader.
func (n *Node) removeServer(nodeID string) error {
	servers, err := n.Configuration()
	if err != nil {
		return err
	}
	server, ok := findServer(servers, nodeID)
	if !ok {
		return nil
	}
	// Raft already refuses a configuration change that would leave no voters
	// (checkConfiguration), so this is not what keeps the cluster electable. It
	// exists to fail before the round trip and to say which node and which rule
	// stopped it: raft's own error dumps the whole configuration struct, which
	// is not much use in an operator's log when `cluster leave` is rejected.
	if server.Suffrage == raft.Voter && countVoters(servers) <= 1 {
		return fmt.Errorf("cluster: refusing to remove %s, it is the last voter", nodeID)
	}
	if len(servers) <= 1 {
		return fmt.Errorf("cluster: refusing to remove %s, it is the last server", nodeID)
	}
	if err := n.raft.RemoveServer(raft.ServerID(nodeID), 0, n.cfg.Timeout).Error(); err != nil {
		return fmt.Errorf("cluster: remove server %s: %w", nodeID, err)
	}
	return nil
}

// transferLeadership hands leadership to another voter when this node leads and
// there is somewhere to hand it to. It is a no-op otherwise.
func (n *Node) transferLeadership() error {
	if !n.IsLeader() {
		return nil
	}
	servers, err := n.Configuration()
	if err != nil {
		return err
	}
	if countVoters(servers) < 2 {
		return nil
	}
	if err := n.raft.LeadershipTransfer().Error(); err != nil {
		if errors.Is(err, raft.ErrLeadershipTransferInProgress) || errors.Is(err, raft.ErrNotLeader) {
			return nil
		}
		if errors.Is(err, raft.ErrRaftShutdown) {
			return nil
		}
		return fmt.Errorf("cluster: transfer leadership: %w", err)
	}
	return nil
}

// handleDiscoveryEvents drains the discovery stream until it closes.
func (n *Node) handleDiscoveryEvents(events <-chan discovery.Event) {
	defer n.wg.Done()
	for event := range events {
		n.handleMembershipEvent(event)
	}
}

// handleMembershipEvent applies one gossip event to the Raft configuration.
// Only the leader mutates configuration; every node tracks addresses.
func (n *Node) handleMembershipEvent(event discovery.Event) {
	member := event.Member
	if member.NodeID == "" {
		return
	}
	switch event.Type {
	case discovery.EventMemberJoin, discovery.EventMemberUpdate:
		n.rememberGRPCAddr(member.NodeID, member.GRPCAddr)
		n.cancelPendingRemoval(member.NodeID)
		n.clearEvicted(member.NodeID)
		n.addDiscoveredMember(member)
	case discovery.EventMemberLeave:
		// A graceful leave is unambiguous: drop the node right away so a
		// rolling replace cannot accumulate dead voters.
		n.cancelPendingRemoval(member.NodeID)
		n.evictMember(member.NodeID)
	case discovery.EventMemberFailed:
		// A suspected failure may be a flap. Give the node a grace period and
		// re-check liveness before evicting it.
		n.scheduleFailedEviction(member.NodeID)
	}
}

// addDiscoveredMember introduces a newly gossiped node to Raft as a non-voter.
// The node promotes itself once it has caught up (see promoteSelf).
func (n *Node) addDiscoveredMember(member discovery.NodeInfo) {
	if member.NodeID == n.cfg.NodeID || member.RaftAddr == "" || !n.IsLeader() {
		return
	}
	servers, err := n.Configuration()
	if err != nil {
		return
	}
	if existing, ok := findServer(servers, member.NodeID); ok {
		if existing.Address == raft.ServerAddress(member.RaftAddr) {
			return
		}
	}
	_ = n.raft.AddNonvoter(raft.ServerID(member.NodeID), raft.ServerAddress(member.RaftAddr), 0, n.cfg.Timeout).Error()
}

// evictMember removes a departed node from the configuration on the leader.
func (n *Node) evictMember(nodeID string) {
	if nodeID == n.cfg.NodeID || !n.IsLeader() {
		return
	}
	n.forgetGRPCAddr(nodeID)
	n.markEvicted(nodeID)
	_ = n.removeServer(nodeID)
}

// markEvicted stops reconciliation from re-adding a node that was deliberately
// removed. A fresh join or update event clears the mark.
func (n *Node) markEvicted(nodeID string) {
	n.mu.Lock()
	n.evicted[nodeID] = struct{}{}
	n.mu.Unlock()
}

func (n *Node) clearEvicted(nodeID string) {
	n.mu.Lock()
	delete(n.evicted, nodeID)
	n.mu.Unlock()
}

func (n *Node) isEvicted(nodeID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	_, evicted := n.evicted[nodeID]
	return evicted
}

// scheduleFailedEviction arms the grace timer for a suspected-failed node. A
// negative FailureGracePeriod disables eviction on failure entirely.
func (n *Node) scheduleFailedEviction(nodeID string) {
	if nodeID == n.cfg.NodeID || n.cfg.FailureGracePeriod < 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return
	}
	if _, pending := n.pendingRemoval[nodeID]; pending {
		return
	}
	n.pendingRemoval[nodeID] = n.after(n.cfg.FailureGracePeriod, func() { n.evictFailedMember(nodeID) })
}

// evictFailedMember runs when the grace period expires. Leadership and
// liveness are re-checked here, not when the timer was armed.
func (n *Node) evictFailedMember(nodeID string) {
	n.mu.Lock()
	delete(n.pendingRemoval, nodeID)
	closed := n.closed
	n.mu.Unlock()
	if closed {
		return
	}
	for _, member := range n.DiscoveryMembers() {
		if member.NodeID != nodeID {
			continue
		}
		if member.Status == discovery.MemberStatusAlive || member.Status == discovery.MemberStatusLeaving {
			return // came back during the grace period
		}
	}
	n.evictMember(nodeID)
}

func (n *Node) cancelPendingRemoval(nodeID string) {
	n.mu.Lock()
	timer, pending := n.pendingRemoval[nodeID]
	delete(n.pendingRemoval, nodeID)
	n.mu.Unlock()
	if pending {
		timer.Stop()
	}
}

func (n *Node) rememberGRPCAddr(nodeID, grpcAddr string) {
	if nodeID == "" || grpcAddr == "" {
		return
	}
	n.mu.Lock()
	n.grpcAddrs[nodeID] = grpcAddr
	n.mu.Unlock()
}

func (n *Node) forgetGRPCAddr(nodeID string) {
	n.mu.Lock()
	delete(n.grpcAddrs, nodeID)
	n.mu.Unlock()
}

// membershipLoop keeps the Raft configuration in step with gossip. On the
// leader it adds members the configuration has not seen; everywhere else it
// drives this node from "unknown to the cluster" to "voter". It runs for the
// lifetime of the node so a lost event or a mistaken eviction self-heals.
func (n *Node) membershipLoop(ctx context.Context) {
	defer n.wg.Done()
	ticker := time.NewTicker(n.cfg.PromotionInterval)
	defer ticker.Stop()
	for {
		n.reconcileDiscovered()
		n.promoteSelf(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// reconcileDiscovered adds alive gossip members the configuration does not know
// about yet. Events alone are not enough: an event that arrives while this node
// is still a candidate would otherwise be dropped and never retried.
func (n *Node) reconcileDiscovered() {
	if !n.IsLeader() {
		return
	}
	members := n.DiscoveryMembers()
	if len(members) == 0 {
		return
	}
	servers, err := n.Configuration()
	if err != nil {
		return
	}
	for _, member := range members {
		if member.NodeID == n.cfg.NodeID || member.RaftAddr == "" {
			continue
		}
		if member.Status != discovery.MemberStatusAlive || n.isEvicted(member.NodeID) {
			continue
		}
		if _, known := findServer(servers, member.NodeID); known {
			continue
		}
		n.rememberGRPCAddr(member.NodeID, member.GRPCAddr)
		_ = n.raft.AddNonvoter(raft.ServerID(member.NodeID), raft.ServerAddress(member.RaftAddr), 0, n.cfg.Timeout).Error()
	}
}

// promoteSelf performs one step of the join/promotion state machine.
func (n *Node) promoteSelf(ctx context.Context) {
	if n.IsLeader() {
		return
	}
	servers, err := n.Configuration()
	if err != nil {
		return
	}
	self, found := findServer(servers, n.cfg.NodeID)
	if found && (self.Suffrage == raft.Voter || n.cfg.NonVoter) {
		return // already at the suffrage this node is meant to have
	}
	if found && !n.caughtUp() {
		return // still replicating; promoting now would put quorum at risk
	}

	leader := n.LeaderGRPCAddr()
	if leader == "" {
		leader = n.cfg.JoinAddr
	}
	if leader == "" || leader == n.cfg.GRPCAddr {
		return
	}
	_ = n.joinRPC(ctx, leader, JoinRequest{
		NodeID:   n.cfg.NodeID,
		RaftAddr: n.raftAdvertiseAddr(),
		GRPCAddr: n.cfg.GRPCAddr,
		NonVoter: !found || n.cfg.NonVoter,
	})
	// The replicated configuration confirms the result on the next tick.
}

// caughtUp reports whether the local FSM has applied everything the leader had
// committed as of the last AppendEntries this node received.
func (n *Node) caughtUp() bool {
	stats := n.raft.Stats()
	commit, err := strconv.ParseUint(stats["commit_index"], 10, 64)
	if err != nil {
		return false
	}
	applied, err := strconv.ParseUint(stats["applied_index"], 10, 64)
	if err != nil {
		return false
	}
	return commit > 0 && applied >= commit
}

// bootstrapExpectLoop waits until BootstrapExpect tagged members are visible
// and then lets exactly one of them — the lowest node ID — bootstrap.
func (n *Node) bootstrapExpectLoop(ctx context.Context) {
	defer n.wg.Done()
	ticker := time.NewTicker(n.cfg.PromotionInterval)
	defer ticker.Stop()
	for {
		if n.tryBootstrapExpect() {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// tryBootstrapExpect reports whether the bootstrap decision has been settled,
// either because this node bootstrapped or because someone else already did.
func (n *Node) tryBootstrapExpect() bool {
	if servers, err := n.Configuration(); err == nil && len(servers) > 0 {
		return true // a configuration exists: bootstrapped here or replicated in
	}

	peers := n.bootstrapCandidates()
	if len(peers) < n.cfg.BootstrapExpect {
		return false
	}
	peers = peers[:n.cfg.BootstrapExpect]
	// Deterministic tiebreak: the lowest node ID in the expected set is the
	// only node that bootstraps, so a symmetric start yields one cluster.
	if peers[0].ID != raft.ServerID(n.cfg.NodeID) {
		return false
	}
	if err := n.raft.BootstrapCluster(raft.Configuration{Servers: peers}).Error(); err != nil {
		return errors.Is(err, raft.ErrCantBootstrap)
	}
	return true
}

// bootstrapCandidates returns this node plus every discovered member that
// advertises a Raft address, sorted by node ID.
func (n *Node) bootstrapCandidates() []raft.Server {
	seen := map[string]struct{}{n.cfg.NodeID: {}}
	peers := []raft.Server{{
		ID:       raft.ServerID(n.cfg.NodeID),
		Address:  raft.ServerAddress(n.raftAdvertiseAddr()),
		Suffrage: raft.Voter,
	}}
	for _, member := range n.DiscoveryMembers() {
		if member.NodeID == "" || member.RaftAddr == "" {
			continue
		}
		if member.Status != discovery.MemberStatusAlive {
			continue
		}
		if _, dup := seen[member.NodeID]; dup {
			continue
		}
		seen[member.NodeID] = struct{}{}
		peers = append(peers, raft.Server{
			ID:       raft.ServerID(member.NodeID),
			Address:  raft.ServerAddress(member.RaftAddr),
			Suffrage: raft.Voter,
		})
	}
	slices.SortFunc(peers, func(a, b raft.Server) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		default:
			return 0
		}
	})
	return peers
}

func (n *Node) raftAdvertiseAddr() string {
	if n.transport != nil {
		return string(n.transport.LocalAddr())
	}
	return n.cfg.RaftAddr
}

func findServer(servers []raft.Server, nodeID string) (raft.Server, bool) {
	for _, server := range servers {
		if server.ID == raft.ServerID(nodeID) {
			return server, true
		}
	}
	return raft.Server{}, false
}

func countVoters(servers []raft.Server) int {
	voters := 0
	for _, server := range servers {
		if server.Suffrage == raft.Voter {
			voters++
		}
	}
	return voters
}
