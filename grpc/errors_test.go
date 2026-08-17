package grpcapi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// panicServiceDesc is a hand-rolled service whose handlers panic, so the
// recovery interceptors can be exercised end to end. db.check asserts with
// panics on hot paths, and on the Raft apply path one malformed request would
// otherwise take down every replica deterministically.
const (
	panicServiceName = "pallasdb.test.PanicService"
	panicUnaryMethod = "/" + panicServiceName + "/Unary"
	panicStreamName  = "Stream"
	panicStreamPath  = "/" + panicServiceName + "/Stream"
)

var panicServiceDesc = grpc.ServiceDesc{
	ServiceName: panicServiceName,
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{{
		MethodName: "Unary",
		Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
			in := new(pbv1.GetRequest)
			if err := dec(in); err != nil {
				return nil, err
			}
			handler := func(context.Context, any) (any, error) { panic("unary handler exploded") }
			if interceptor == nil {
				return handler(ctx, in)
			}
			info := &grpc.UnaryServerInfo{Server: srv, FullMethod: panicUnaryMethod}
			return interceptor(ctx, in, info, handler)
		},
	}},
	Streams: []grpc.StreamDesc{{
		StreamName:    panicStreamName,
		ServerStreams: true,
		Handler: func(any, grpc.ServerStream) error {
			panic("stream handler exploded")
		},
	}},
}

func TestRecoveryInterceptorConvertsPanics(t *testing.T) {
	_, dial, srv := newTestServer(t)
	srv.RegisterService(&panicServiceDesc, struct{}{})
	conn := dial(grpc.WithTransportCredentials(insecure.NewCredentials()))
	ctx := testContext(t)

	err := conn.Invoke(ctx, panicUnaryMethod, &pbv1.GetRequest{Key: []byte("k")}, &pbv1.GetResponse{})
	require.Equal(t, codes.Internal, status.Code(err))
	require.NotContains(t, status.Convert(err).Message(), "exploded", "the panic value must not leak to clients")

	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{StreamName: panicStreamName, ServerStreams: true}, panicStreamPath)
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&pbv1.GetRequest{Key: []byte("k")}))
	err = stream.RecvMsg(&pbv1.GetResponse{})
	require.Equal(t, codes.Internal, status.Code(err))

	// The server is still serving after both panics.
	client := pbv1.NewKVServiceClient(conn)
	_, err = client.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.NoError(t, err)
}

func TestRaftErrorCodeMapping(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want codes.Code
	}{
		{raft.ErrNotLeader, codes.Unavailable},
		{raft.ErrLeadershipLost, codes.Unavailable},
		{raft.ErrRaftShutdown, codes.Unavailable},
		{raft.ErrLeadershipTransferInProgress, codes.Unavailable},
		{raft.ErrEnqueueTimeout, codes.DeadlineExceeded},
		{raft.ErrAbortedByRestore, codes.Aborted},
		{errors.New("disk on fire"), codes.Internal},
		{fmt.Errorf("wrapped: %w", raft.ErrLeadershipLost), codes.Unavailable},
		{fmt.Errorf("wrapped: %w", raft.ErrEnqueueTimeout), codes.DeadlineExceeded},
		{nil, codes.OK},
	} {
		require.Equalf(t, tc.want, raftErrorCode(tc.err), "error %v", tc.err)
	}
}

func TestRaftStatusErrorCarriesLeaderRedirect(t *testing.T) {
	leader := leaderInfo{ID: "node-2", GRPCAddr: "10.0.0.4:50051", RaftAddr: "10.0.0.4:7001"}

	err := raftStatusError("raft apply", raft.ErrLeadershipLost, leader)
	require.Equal(t, codes.Unavailable, status.Code(err))
	nodeID, grpcAddr, ok := LeaderFromError(err)
	require.True(t, ok)
	require.Equal(t, "node-2", nodeID)
	require.Equal(t, "10.0.0.4:50051", grpcAddr)

	// The dialable address, never the Raft transport port, is what a client is
	// told to retry against.
	redirect := notLeaderError(leader)
	require.Equal(t, codes.Unavailable, status.Code(redirect))
	require.Contains(t, status.Convert(redirect).Message(), "10.0.0.4:50051")
	require.NotContains(t, status.Convert(redirect).Message(), "7001")

	// A timeout is not a redirect: enqueue timeouts carry no leader details.
	timeout := raftStatusError("raft apply", raft.ErrEnqueueTimeout, leader)
	require.Equal(t, codes.DeadlineExceeded, status.Code(timeout))
	_, _, ok = LeaderFromError(timeout)
	require.False(t, ok)

	unknown := notLeaderError(leaderInfo{})
	require.Equal(t, codes.Unavailable, status.Code(unknown))
	_, _, ok = LeaderFromError(unknown)
	require.False(t, ok)
}

// A caller's cancellation must be reported as such rather than blamed on Raft.
func TestRaftStatusErrorHonoursContext(t *testing.T) {
	require.Equal(t, codes.Canceled, status.Code(raftStatusError("raft apply", context.Canceled, leaderInfo{})))
	require.Equal(t, codes.DeadlineExceeded, status.Code(raftStatusError("raft apply", context.DeadlineExceeded, leaderInfo{})))
}

func TestApplyDeadlineHonoursClientContext(t *testing.T) {
	node := clusterNode{timeout: time.Minute}

	require.Equal(t, time.Minute, node.applyDeadline(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	require.Less(t, node.applyDeadline(ctx), time.Minute)

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()
	require.Negative(t, node.applyDeadline(expired))
}
