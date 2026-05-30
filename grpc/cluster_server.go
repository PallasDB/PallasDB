package grpcapi

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/cluster"
	"github.com/teddymalhan/pallasdb/cluster/discovery"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// ClusterKVServer routes writes through Raft and reads through a barrier+FSM.
type ClusterKVServer struct {
	fsm     *cluster.FSM
	raft    *raft.Raft
	timeout time.Duration
	pbv1.UnimplementedKVServiceServer
}

func NewClusterGRPCServer(node *cluster.Node, applyTimeout time.Duration, opts ...grpc.ServerOption) *grpc.Server {
	srv := grpc.NewServer(opts...)
	pbv1.RegisterKVServiceServer(srv, &ClusterKVServer{fsm: node.FSM(), raft: node.Raft(), timeout: applyTimeout})
	pbv1.RegisterClusterServiceServer(srv, &ClusterManagementServer{node: node, raft: node.Raft(), timeout: applyTimeout})
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, healthSrv)
	return srv
}

// ClusterManagementServer serves Raft membership operations over gRPC.
type ClusterManagementServer struct {
	node    *cluster.Node
	raft    *raft.Raft
	timeout time.Duration
	pbv1.UnimplementedClusterServiceServer
}

func (s *ClusterManagementServer) Join(ctx context.Context, req *pbv1.JoinRequest) (*pbv1.JoinResponse, error) {
	if req.GetNodeId() == "" || req.GetRaftAddr() == "" {
		return nil, status.Error(codes.InvalidArgument, "node_id and raft_addr are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if s.raft.State() != raft.Leader {
		leader, _ := s.raft.LeaderWithID()
		return nil, status.Errorf(codes.Unavailable, "not leader; try %s", leader)
	}
	f := s.raft.AddVoter(raft.ServerID(req.GetNodeId()), raft.ServerAddress(req.GetRaftAddr()), 0, s.timeout)
	if err := f.Error(); err != nil {
		return nil, status.Errorf(codes.Internal, "add voter: %v", err)
	}
	return &pbv1.JoinResponse{}, nil
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
		member.GrpcAddr = discovered.GRPCAddr
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

func (s *ClusterManagementServer) GetLeader(ctx context.Context, _ *pbv1.GetLeaderRequest) (*pbv1.GetLeaderResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	addr, id := s.raft.LeaderWithID()
	return &pbv1.GetLeaderResponse{NodeId: string(id), RaftAddr: string(addr)}, nil
}

func (s *ClusterManagementServer) raftMembers() ([]raft.Server, error) {
	future := s.raft.GetConfiguration()
	if err := future.Error(); err != nil {
		return nil, status.Errorf(codes.Unavailable, "raft configuration: %v", err)
	}
	return future.Configuration().Servers, nil
}

func clusterMemberStatus(status discovery.MemberStatus) pbv1.ClusterMemberStatus {
	switch status {
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

func (s *ClusterKVServer) applyCommand(cmd cluster.Command) (*cluster.FSMResult, error) {
	if s.raft.State() != raft.Leader {
		leader, _ := s.raft.LeaderWithID()
		return nil, status.Errorf(codes.Unavailable, "not leader; try %s", leader)
	}
	data, err := cluster.EncodeCommand(cmd)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode command: %v", err)
	}
	f := s.raft.Apply(data, s.timeout)
	if err := f.Error(); err != nil {
		return nil, status.Errorf(codes.Internal, "raft apply: %v", err)
	}
	res, ok := f.Response().(*cluster.FSMResult)
	if !ok {
		return nil, status.Errorf(codes.Internal, "unexpected fsm result type")
	}
	if res.Err != nil {
		return nil, status.Errorf(codes.Internal, "fsm apply: %v", res.Err)
	}
	return res, nil
}

func (s *ClusterKVServer) barrier() error {
	if f := s.raft.Barrier(s.timeout); f.Error() != nil {
		return status.Errorf(codes.Unavailable, "raft barrier: %v", f.Error())
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
	if err := s.barrier(); err != nil {
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
	res, err := s.applyCommand(cluster.Command{
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

func (s *ClusterKVServer) Delete(ctx context.Context, req *pbv1.DeleteRequest) (*pbv1.DeleteResponse, error) {
	if err := validateKey(req.GetKey()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	res, err := s.applyCommand(cluster.Command{Op: cluster.OpDel, Key: req.GetKey()})
	if err != nil {
		return nil, err
	}
	if !res.Updated {
		return nil, status.Error(codes.NotFound, "key not found")
	}
	return &pbv1.DeleteResponse{Deleted: res.Updated}, nil
}

func (s *ClusterKVServer) Range(req *pbv1.RangeRequest, stream grpc.ServerStreamingServer[pbv1.RangeResponse]) error {
	if len(req.GetStart()) == 0 || len(req.GetStop()) == 0 {
		return status.Error(codes.InvalidArgument, "start and stop are required")
	}
	if err := s.barrier(); err != nil {
		return err
	}
	tx, release := s.fsm.NewTX()
	defer release()

	iter, err := tx.Range(req.GetStart(), req.GetStop(), req.GetDescending())
	if err != nil {
		return status.Errorf(codes.Internal, "range keys: %v", err)
	}
	for ; iter.Valid(); err = iter.Next() {
		if err != nil {
			return status.Errorf(codes.Internal, "iterate range: %v", err)
		}
		if err := stream.Context().Err(); err != nil {
			return status.FromContextError(err).Err()
		}
		if err := stream.Send(&pbv1.RangeResponse{Key: iter.Key(), Value: iter.Val()}); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return nil
}
