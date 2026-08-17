package grpcapi

import (
	"context"
	"time"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/cluster"
	"github.com/teddymalhan/pallasdb/cluster/discovery"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

// ClusterKVServer routes writes through Raft and serves reads from the local
// FSM at the consistency level the client asked for.
type ClusterKVServer struct {
	clusterNode
	fsm    *cluster.FSM
	ranges rangeLimits
	pbv1.UnimplementedKVServiceServer
}

// ClusterManagementServer serves Raft membership operations over gRPC.
type ClusterManagementServer struct {
	clusterNode
	pbv1.UnimplementedClusterServiceServer
}

// NewClusterGRPCServer builds the clustered server.
//
// As with NewGRPCServer the variadic list takes both PallasDB Options and raw
// grpc.ServerOption values. Membership RPCs are unauthenticated unless
// WithAuthToken or WithMTLSAuth is supplied: a Join hands the caller a full
// replica and a vote, so a cluster reachable from anywhere untrusted must set
// one of them.
func NewClusterGRPCServer(node *cluster.Node, applyTimeout time.Duration, opts ...grpc.ServerOption) *grpc.Server {
	cfg, grpcOpts := buildConfig(opts)
	srv := grpc.NewServer(grpcOpts...)

	shared := clusterNode{node: node, raft: node.Raft(), timeout: applyTimeout}
	pbv1.RegisterKVServiceServer(srv, &ClusterKVServer{clusterNode: shared, fsm: node.FSM(), ranges: cfg.ranges})
	pbv1.RegisterClusterServiceServer(srv, &ClusterManagementServer{clusterNode: shared})

	services := []string{
		pbv1.KVService_ServiceDesc.ServiceName,
		pbv1.ClusterService_ServiceDesc.ServiceName,
	}
	if cfg.sql != nil {
		pbv1.RegisterSQLServiceServer(srv, NewSQLServer(cfg.sql))
		services = append(services, pbv1.SQLService_ServiceDesc.ServiceName)
	}

	healthSrv := newHealthServer(services)
	healthpb.RegisterHealthServer(srv, healthSrv)
	go watchLeadershipHealth(cfg.ctx, healthSrv, shared.raft)

	if cfg.reflection {
		reflection.Register(srv)
	}
	return srv
}

// Join adds a peer to the cluster. It must run on the leader; a follower
// answers with a redirect naming the leader's gRPC address.
func (s *ClusterManagementServer) Join(ctx context.Context, req *pbv1.JoinRequest) (*pbv1.JoinResponse, error) {
	if req.GetNodeId() == "" || req.GetRaftAddr() == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and raft_addr are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.raft.State() != raft.Leader {
		return nil, notLeaderError(s.leader())
	}
	if err := s.join(req.GetNodeId(), req.GetRaftAddr(), req.GetGrpcAddr(), req.GetNonVoter()); err != nil {
		return nil, membershipStatusError("join cluster", err, s.leader())
	}
	return &pbv1.JoinResponse{}, nil
}

// membershipStatusError maps a membership failure. A failure the Raft sentinels
// do not explain, raised while no leader is known, is a leadership problem
// rather than an internal one: there is nobody to route the change to, so the
// client should retry rather than give up.
func membershipStatusError(op string, err error, leader leaderInfo) error {
	if !leader.known() && raftErrorCode(err) == codes.Internal {
		return status.Errorf(codes.Unavailable, "%s: %v (no leader elected)", op, err)
	}
	return raftStatusError(op, err, leader)
}

// Leave removes a peer. An empty node_id means "remove me". Removing a node
// that is already gone succeeds: membership changes get retried.
func (s *ClusterManagementServer) Leave(ctx context.Context, req *pbv1.LeaveRequest) (*pbv1.LeaveResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if req.GetNodeId() != "" {
		member, err := s.isMember(req.GetNodeId())
		if err != nil {
			return nil, raftStatusError("raft configuration", err, s.leader())
		}
		if !member {
			return &pbv1.LeaveResponse{}, nil
		}
	}
	// Self-eviction is legitimate on a follower, and the node routes it to the
	// leader itself; the raft fallback cannot, so it needs the leader here.
	if !s.routesLeaveItself() && s.raft.State() != raft.Leader {
		return nil, notLeaderError(s.leader())
	}
	if err := s.leave(req.GetNodeId()); err != nil {
		return nil, membershipStatusError("leave cluster", err, s.leader())
	}
	return &pbv1.LeaveResponse{}, nil
}

func (s *ClusterManagementServer) ListMembers(ctx context.Context, _ *pbv1.ListMembersRequest) (*pbv1.ListMembersResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}

	leaderAddr, leaderID := s.raft.LeaderWithID()
	raftMembers, err := s.raftMembers()
	if err != nil {
		return nil, err
	}

	membersByID := make(map[string]*pbv1.ClusterMember, len(raftMembers))
	for _, server := range raftMembers {
		member := &pbv1.ClusterMember{
			NodeId:    string(server.ID),
			RaftAddr:  string(server.Address),
			GrpcAddr:  s.grpcAddr(string(server.ID), string(server.Address)),
			RaftVoter: server.Suffrage == raft.Voter,
			Leader:    server.ID == leaderID,
		}
		membersByID[member.NodeId] = member
	}

	for _, discovered := range s.node.DiscoveryMembers() {
		member := membersByID[discovered.NodeID]
		if member == nil {
			member = &pbv1.ClusterMember{NodeId: discovered.NodeID}
			membersByID[discovered.NodeID] = member
		}
		if discovered.GRPCAddr != "" {
			member.GrpcAddr = discovered.GRPCAddr
		}
		member.SerfAddr = discovered.SerfAddr
		if member.RaftAddr == "" {
			member.RaftAddr = discovered.RaftAddr
		}
		member.Status = clusterMemberStatus(discovered.Status)
		member.Leader = member.Leader || raft.ServerAddress(discovered.RaftAddr) == leaderAddr
	}

	members := make([]*pbv1.ClusterMember, 0, len(membersByID))
	for _, member := range membersByID {
		members = append(members, member)
	}
	return &pbv1.ListMembersResponse{Members: members}, nil
}

// GetLeader reports the current leader, including the address a client can
// actually dial. Raft's transport address is reported too, but only as a hint:
// it does not speak gRPC.
func (s *ClusterManagementServer) GetLeader(ctx context.Context, _ *pbv1.GetLeaderRequest) (*pbv1.GetLeaderResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	leader := s.leader()
	return &pbv1.GetLeaderResponse{
		NodeId:   leader.ID,
		RaftAddr: leader.RaftAddr,
		GrpcAddr: leader.GRPCAddr,
	}, nil
}

func (s *ClusterManagementServer) raftMembers() ([]raft.Server, error) {
	future := s.raft.GetConfiguration()
	if err := future.Error(); err != nil {
		return nil, status.Errorf(codes.Unavailable, "raft configuration: %v", err)
	}
	return future.Configuration().Servers, nil
}

func clusterMemberStatus(memberStatus discovery.MemberStatus) pbv1.ClusterMemberStatus {
	switch memberStatus {
	case discovery.MemberStatusAlive:
		return pbv1.ClusterMemberStatus_CLUSTER_MEMBER_STATUS_ALIVE
	case discovery.MemberStatusLeaving:
		return pbv1.ClusterMemberStatus_CLUSTER_MEMBER_STATUS_LEAVING
	case discovery.MemberStatusLeft:
		return pbv1.ClusterMemberStatus_CLUSTER_MEMBER_STATUS_LEFT
	case discovery.MemberStatusFailed:
		return pbv1.ClusterMemberStatus_CLUSTER_MEMBER_STATUS_FAILED
	default:
		return pbv1.ClusterMemberStatus_CLUSTER_MEMBER_STATUS_UNSPECIFIED
	}
}

// applyCommand replicates a mutation through Raft.
func (s *ClusterKVServer) applyCommand(ctx context.Context, cmd cluster.Command) (*cluster.FSMResult, error) {
	if s.raft.State() != raft.Leader {
		return nil, notLeaderError(s.leader())
	}
	data, err := cluster.EncodeCommand(cmd)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode command: %v", err)
	}
	timeout := s.applyDeadline(ctx)
	if timeout <= 0 {
		return nil, status.Error(codes.DeadlineExceeded, "raft apply: deadline already expired")
	}

	future := s.raft.Apply(data, timeout)
	if err := waitFuture(ctx, future); err != nil {
		return nil, raftStatusError("raft apply", err, s.leader())
	}
	res, ok := future.Response().(*cluster.FSMResult)
	if !ok {
		return nil, status.Error(codes.Internal, "unexpected fsm result type")
	}
	if res.Err != nil {
		return nil, status.Errorf(codes.Internal, "fsm apply: %v", res.Err)
	}
	return res, nil
}

// readReady makes the local FSM safe to read at the requested consistency.
//
// LINEARIZABLE confirms leadership with a heartbeat round (VerifyLeader, no log
// write) and then waits for the FSM to catch up to the commit index, so a read
// costs no disk write anywhere in the cluster. STALE reads the local FSM
// directly and is servable by any node, including one that has lost quorum.
//
// Waiting on the commit index only terminates because the leader fences its
// applied index once per term (cluster.Node.fenceAppliedIndex): raft appends a
// no-op per term that it never routes to an FSM, so the applied index cannot
// reach the commit index on its own until some later write closes the gap.
//
// An FSM that does not report an applied index falls back to the Raft barrier:
// slower, but never less correct.
func (s *ClusterKVServer) readReady(ctx context.Context, consistency pbv1.Consistency) error {
	if consistency == pbv1.Consistency_CONSISTENCY_STALE {
		return nil
	}
	waiter, ok := any(s.fsm).(appliedIndexWaiter)
	if !ok {
		return s.barrier(ctx)
	}
	if err := waitFuture(ctx, s.raft.VerifyLeader()); err != nil {
		return raftStatusError("verify leader", err, s.leader())
	}
	if err := waiter.WaitForAppliedIndex(ctx, s.raft.CommitIndex()); err != nil {
		return raftStatusError("await applied index", err, s.leader())
	}
	return nil
}

// barrier is the fallback linearizable read path. It commits a real (empty) log
// entry, which every replica fsyncs, so it is a quorum round trip plus a disk
// write per read.
func (s *ClusterKVServer) barrier(ctx context.Context) error {
	timeout := s.applyDeadline(ctx)
	if timeout <= 0 {
		return status.Error(codes.DeadlineExceeded, "raft barrier: deadline already expired")
	}
	if err := waitFuture(ctx, s.raft.Barrier(timeout)); err != nil {
		return raftStatusError("raft barrier", err, s.leader())
	}
	return nil
}

func (s *ClusterKVServer) Get(ctx context.Context, req *pbv1.GetRequest) (*pbv1.GetResponse, error) {
	if err := validateKey(req.GetKey()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if err := s.readReady(ctx, req.GetConsistency()); err != nil {
		return nil, err
	}
	val, ok, err := s.fsm.Get(req.GetKey())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get key: %v", err)
	}
	if !ok {
		return nil, status.Error(codes.NotFound, "key not found")
	}
	return &pbv1.GetResponse{Value: val}, nil
}

func (s *ClusterKVServer) Put(ctx context.Context, req *pbv1.PutRequest) (*pbv1.PutResponse, error) {
	if err := validateKey(req.GetKey()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	mode, err := updateMode(req.GetMode())
	if err != nil {
		return nil, err
	}
	res, err := s.applyCommand(ctx, cluster.Command{
		Op:   cluster.OpPut,
		Key:  req.GetKey(),
		Val:  req.GetValue(),
		Mode: int(mode),
	})
	if err != nil {
		return nil, err
	}
	return &pbv1.PutResponse{Updated: res.Updated}, nil
}

// Delete removes a key. As on the single-node server, deleting an absent key
// reports deleted=false rather than failing, so a retried delete is safe.
func (s *ClusterKVServer) Delete(ctx context.Context, req *pbv1.DeleteRequest) (*pbv1.DeleteResponse, error) {
	if err := validateKey(req.GetKey()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	res, err := s.applyCommand(ctx, cluster.Command{Op: cluster.OpDel, Key: req.GetKey()})
	if err != nil {
		return nil, err
	}
	return &pbv1.DeleteResponse{Deleted: res.Updated}, nil
}

func (s *ClusterKVServer) Range(req *pbv1.RangeRequest, stream grpc.ServerStreamingServer[pbv1.RangeResponse]) error {
	if err := s.readReady(stream.Context(), req.GetConsistency()); err != nil {
		return err
	}
	tx, release := s.fsm.NewTX()
	defer release()

	return ignoreClosedStream(newRangeScan(req, s.ranges).stream(tx, stream))
}
