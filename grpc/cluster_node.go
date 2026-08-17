package grpcapi

import (
	"context"
	"time"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/cluster"
)

// clusterNode is the cluster.Node surface the gRPC layer needs.
//
// A few accessors on cluster.Node are probed at runtime rather than called
// directly: they are landing in the cluster package alongside this work, and
// probing keeps this package buildable and correct either way. Each probe has a
// fallback that is exactly today's behaviour, so nothing here is ever less
// correct than the direct call it stands in for.
type clusterNode struct {
	node    *cluster.Node
	raft    *raft.Raft
	timeout time.Duration
}

// Accessors expected on *cluster.Node.
type (
	grpcAddrResolver interface{ GRPCAddrForID(id string) string }
	leaderGRPCAddrer interface{ LeaderGRPCAddr() string }
	nodeJoiner       interface {
		Join(nodeID, raftAddr, grpcAddr string, nonVoter bool) error
	}
	nodeLeaver interface {
		Leave(nodeID string) error
		LeaveSelf() error
	}
)

// appliedIndexWaiter is the FSM accessor that makes a linearizable read cheap:
// it lets the server wait for the FSM to catch up to the current commit index
// instead of writing a Raft barrier entry.
type appliedIndexWaiter interface {
	WaitForAppliedIndex(ctx context.Context, index uint64) error
}

// leader identifies the node a client should be redirected to.
func (c clusterNode) leader() leaderInfo {
	raftAddr, id := c.raft.LeaderWithID()
	info := leaderInfo{ID: string(id), RaftAddr: string(raftAddr)}
	if resolver, ok := any(c.node).(leaderGRPCAddrer); ok {
		info.GRPCAddr = resolver.LeaderGRPCAddr()
	}
	if info.GRPCAddr == "" {
		info.GRPCAddr = c.grpcAddr(info.ID, info.RaftAddr)
	}
	return info
}

// grpcAddr maps a node identity to the address clients dial. The Raft transport
// address is never a usable answer, so an unknown node yields an empty string
// rather than a misleading one.
func (c clusterNode) grpcAddr(nodeID, raftAddr string) string {
	if c.node == nil {
		return ""
	}
	if resolver, ok := any(c.node).(grpcAddrResolver); ok && nodeID != "" {
		if addr := resolver.GRPCAddrForID(nodeID); addr != "" {
			return addr
		}
	}
	for _, member := range c.node.DiscoveryMembers() {
		if member.NodeID == nodeID || (nodeID == "" && member.RaftAddr == raftAddr && raftAddr != "") {
			return member.GRPCAddr
		}
	}
	return ""
}

// join adds a peer. The advertised gRPC address is recorded when the node
// supports it, so later redirects can name a dialable endpoint.
func (c clusterNode) join(nodeID, raftAddr, grpcAddr string, nonVoter bool) error {
	if joiner, ok := any(c.node).(nodeJoiner); ok {
		return joiner.Join(nodeID, raftAddr, grpcAddr, nonVoter)
	}
	id, addr := raft.ServerID(nodeID), raft.ServerAddress(raftAddr)
	if nonVoter {
		return c.raft.AddNonvoter(id, addr, 0, c.timeout).Error()
	}
	return c.raft.AddVoter(id, addr, 0, c.timeout).Error()
}

// leave removes a peer from the configuration. Removing a node that is not a
// member is not an error: membership changes are retried by operators and
// scripts, and the caller's intent is already satisfied.
func (c clusterNode) leave(nodeID string) error {
	if leaver, ok := any(c.node).(nodeLeaver); ok {
		if nodeID == "" {
			return leaver.LeaveSelf()
		}
		return leaver.Leave(nodeID)
	}
	return c.raft.RemoveServer(raft.ServerID(nodeID), 0, c.timeout).Error()
}

// routesLeaveItself reports whether cluster.Node can route a membership
// removal on its own. The raft fallback cannot: RemoveServer must run on the
// leader.
func (c clusterNode) routesLeaveItself() bool {
	_, ok := any(c.node).(nodeLeaver)
	return ok
}

// isMember reports whether nodeID appears in the committed configuration.
func (c clusterNode) isMember(nodeID string) (bool, error) {
	future := c.raft.GetConfiguration()
	if err := future.Error(); err != nil {
		return false, err
	}
	for _, server := range future.Configuration().Servers {
		if string(server.ID) == nodeID {
			return true, nil
		}
	}
	return false, nil
}

// applyDeadline honours the client's deadline: a server-wide apply timeout that
// outlives the caller's context only wastes a log entry nobody is waiting for.
func (c clusterNode) applyDeadline(ctx context.Context) time.Duration {
	timeout := c.timeout
	deadline, ok := ctx.Deadline()
	if !ok {
		return timeout
	}
	remaining := time.Until(deadline)
	if timeout <= 0 || remaining < timeout {
		return remaining
	}
	return timeout
}

// waitFuture blocks on a Raft future without ignoring the caller's context.
// The future itself is already bounded by the timeout it was created with.
func waitFuture(ctx context.Context, future raft.Future) error {
	done := make(chan error, 1)
	go func() { done <- future.Error() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
