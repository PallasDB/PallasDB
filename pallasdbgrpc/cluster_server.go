package pallasdbgrpc

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/hashicorp/raft"
	"github.com/teddymalhan/pallasdb/cluster"
	pallasdbv1 "github.com/teddymalhan/pallasdb/pallasdbpb/v1"
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
	pallasdbv1.UnimplementedKVServiceServer
}

func NewClusterGRPCServer(fsm *cluster.FSM, r *raft.Raft, applyTimeout time.Duration, opts ...grpc.ServerOption) *grpc.Server {
	srv := grpc.NewServer(opts...)
	pallasdbv1.RegisterKVServiceServer(srv, &ClusterKVServer{fsm: fsm, raft: r, timeout: applyTimeout})
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, healthSrv)
	return srv
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

func (s *ClusterKVServer) Get(ctx context.Context, req *pallasdbv1.GetRequest) (*pallasdbv1.GetResponse, error) {
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
	return &pallasdbv1.GetResponse{Value: val}, nil
}

func (s *ClusterKVServer) Put(ctx context.Context, req *pallasdbv1.PutRequest) (*pallasdbv1.PutResponse, error) {
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
	return &pallasdbv1.PutResponse{Updated: res.Updated}, nil
}

func (s *ClusterKVServer) Delete(ctx context.Context, req *pallasdbv1.DeleteRequest) (*pallasdbv1.DeleteResponse, error) {
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
	return &pallasdbv1.DeleteResponse{Deleted: res.Updated}, nil
}

func (s *ClusterKVServer) Range(req *pallasdbv1.RangeRequest, stream grpc.ServerStreamingServer[pallasdbv1.RangeResponse]) error {
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
		if err := stream.Send(&pallasdbv1.RangeResponse{Key: iter.Key(), Value: iter.Val()}); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return nil
}
