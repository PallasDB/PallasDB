package grpcapi

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/teddymalhan/pallasdb/db"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
)

const defaultGracefulStopTimeout = 15 * time.Second

// KVServer exposes a db.KV over gRPC.
type KVServer struct {
	store  *db.KV
	ranges rangeLimits
	pbv1.UnimplementedKVServiceServer
}

// NewKVServer builds a KV service with the default scan bounds.
func NewKVServer(store *db.KV) *KVServer {
	return &KVServer{store: store, ranges: defaultConfig().ranges}
}

// Register adds the KV service to s.
func Register(s grpc.ServiceRegistrar, store *db.KV) {
	pbv1.RegisterKVServiceServer(s, NewKVServer(store))
}

// NewGRPCServer builds the single-node server.
//
// The variadic list accepts both PallasDB Options (WithTLS, WithLogger,
// WithMetrics, WithSQL, ...) and plain grpc.ServerOption values. Everything
// beyond panic recovery and the message/stream limits is off by default, so a
// local single-node server needs no configuration at all.
func NewGRPCServer(store *db.KV, opts ...grpc.ServerOption) *grpc.Server {
	cfg, grpcOpts := buildConfig(opts)
	srv := grpc.NewServer(grpcOpts...)

	pbv1.RegisterKVServiceServer(srv, &KVServer{store: store, ranges: cfg.ranges})
	services := []string{pbv1.KVService_ServiceDesc.ServiceName}

	if cfg.sql != nil {
		pbv1.RegisterSQLServiceServer(srv, NewSQLServer(cfg.sql))
		services = append(services, pbv1.SQLService_ServiceDesc.ServiceName)
	}

	healthpb.RegisterHealthServer(srv, newHealthServer(services))
	if cfg.reflection {
		reflection.Register(srv)
	}
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

// Get reads a key. GetRequest.consistency is accepted and ignored: a
// single-node store is always current with respect to its own writes.
func (s *KVServer) Get(ctx context.Context, req *pbv1.GetRequest) (*pbv1.GetResponse, error) {
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
	return &pbv1.GetResponse{Value: value}, nil
}

func (s *KVServer) Put(ctx context.Context, req *pbv1.PutRequest) (*pbv1.PutResponse, error) {
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
	return &pbv1.PutResponse{Updated: updated}, nil
}

// Delete removes a key. Deleting an absent key is not an error: the RPC is
// idempotent, so a retry after a lost response reports deleted=false rather
// than failing a delete that already succeeded.
func (s *KVServer) Delete(ctx context.Context, req *pbv1.DeleteRequest) (*pbv1.DeleteResponse, error) {
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
	return &pbv1.DeleteResponse{Deleted: deleted}, nil
}

// Range streams a key range. An empty start scans from the first key and an
// empty stop scans to the last, so prefix and whole-keyspace scans are
// expressible; the stream is bounded by the request limit and the server's scan
// deadline so it cannot pin the KV snapshot indefinitely.
func (s *KVServer) Range(req *pbv1.RangeRequest, stream grpc.ServerStreamingServer[pbv1.RangeResponse]) error {
	tx := s.store.NewTX()
	defer tx.Abort()

	return ignoreClosedStream(newRangeScan(req, s.ranges).stream(tx, stream))
}

func validateKey(key []byte) error {
	if len(key) == 0 {
		return status.Error(codes.InvalidArgument, "key is required")
	}
	return nil
}

func updateMode(mode pbv1.PutMode) (db.UpdateMode, error) {
	switch mode {
	case pbv1.PutMode_PUT_MODE_UPSERT, pbv1.PutMode_PUT_MODE_UNSPECIFIED:
		return db.ModeUpsert, nil
	case pbv1.PutMode_PUT_MODE_INSERT:
		return db.ModeInsert, nil
	case pbv1.PutMode_PUT_MODE_UPDATE:
		return db.ModeUpdate, nil
	default:
		return db.ModeUnknown, status.Error(codes.InvalidArgument, "invalid put mode")
	}
}
