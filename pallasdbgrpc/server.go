package pallasdbgrpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/teddymalhan/pallasdb/pallasdb"
	pallasdbv1 "github.com/teddymalhan/pallasdb/pallasdbpb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

const defaultGracefulStopTimeout = 15 * time.Second

// KVServer exposes a pallasdb.KV over gRPC.
type KVServer struct {
	store *pallasdb.KV
	pallasdbv1.UnimplementedKVServiceServer
}

func NewKVServer(store *pallasdb.KV) *KVServer {
	return &KVServer{store: store}
}

func Register(s grpc.ServiceRegistrar, store *pallasdb.KV) {
	pallasdbv1.RegisterKVServiceServer(s, NewKVServer(store))
}

func NewGRPCServer(store *pallasdb.KV, opts ...grpc.ServerOption) *grpc.Server {
	srv := grpc.NewServer(opts...)
	Register(srv, store)
	healthSrv := health.NewServer()
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, healthSrv)
	return srv
}

func Serve(ctx context.Context, lis net.Listener, srv *grpc.Server) error {
	return ServeWithGracefulStopTimeout(ctx, lis, srv, defaultGracefulStopTimeout)
}

func ServeWithGracefulStopTimeout(ctx context.Context, lis net.Listener, srv *grpc.Server, timeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		GracefulStop(srv, timeout)
		return ctx.Err()
	}
}

func GracefulStop(srv *grpc.Server, timeout time.Duration) {
	stopped := make(chan struct{})
	go func() {
		srv.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(timeout):
		srv.Stop()
	}
}

func LoggingUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = slog.Default()
	}
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logger.InfoContext(ctx, "grpc request",
			"method", info.FullMethod,
			"code", status.Code(err).String(),
			"duration", time.Since(start),
		)
		return resp, err
	}
}

func (s *KVServer) Get(ctx context.Context, req *pallasdbv1.GetRequest) (*pallasdbv1.GetResponse, error) {
	if err := validateKey(req.GetKey()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}

	value, ok, err := s.store.Get(req.GetKey())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get key: %v", err)
	}
	if !ok {
		return nil, status.Error(codes.NotFound, "key not found")
	}
	return &pallasdbv1.GetResponse{Value: value}, nil
}

func (s *KVServer) Put(ctx context.Context, req *pallasdbv1.PutRequest) (*pallasdbv1.PutResponse, error) {
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
	updated, err := s.store.SetEx(req.GetKey(), req.GetValue(), mode)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "put key: %v", err)
	}
	return &pallasdbv1.PutResponse{Updated: updated}, nil
}

func (s *KVServer) Delete(ctx context.Context, req *pallasdbv1.DeleteRequest) (*pallasdbv1.DeleteResponse, error) {
	if err := validateKey(req.GetKey()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}

	deleted, err := s.store.Del(req.GetKey())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "delete key: %v", err)
	}
	if !deleted {
		return nil, status.Error(codes.NotFound, "key not found")
	}
	return &pallasdbv1.DeleteResponse{Deleted: deleted}, nil
}

func (s *KVServer) Range(req *pallasdbv1.RangeRequest, stream grpc.ServerStreamingServer[pallasdbv1.RangeResponse]) error {
	if len(req.GetStart()) == 0 || len(req.GetStop()) == 0 {
		return status.Error(codes.InvalidArgument, "start and stop are required")
	}

	tx := s.store.NewTX()
	defer tx.Abort()

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

func validateKey(key []byte) error {
	if len(key) == 0 {
		return status.Error(codes.InvalidArgument, "key is required")
	}
	return nil
}

func updateMode(mode pallasdbv1.PutMode) (pallasdb.UpdateMode, error) {
	switch mode {
	case pallasdbv1.PutMode_PUT_MODE_UPSERT, pallasdbv1.PutMode_PUT_MODE_UNSPECIFIED:
		return pallasdb.ModeUpsert, nil
	case pallasdbv1.PutMode_PUT_MODE_INSERT:
		return pallasdb.ModeInsert, nil
	case pallasdbv1.PutMode_PUT_MODE_UPDATE:
		return pallasdb.ModeUpdate, nil
	default:
		return pallasdb.ModeUnknown, status.Error(codes.InvalidArgument, "invalid put mode")
	}
}
