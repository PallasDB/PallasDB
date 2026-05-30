package gokvgrpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teddymalhan/gokv/gokv"
	gokvv1 "github.com/teddymalhan/gokv/gokvpb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func newTestClient(t *testing.T) (gokvv1.KVServiceClient, func()) {
	t.Helper()

	store, err := gokv.NewKV(t.TempDir())
	require.NoError(t, err)

	lis := bufconn.Listen(1024 * 1024)
	srv := NewGRPCServer(store)
	go func() {
		_ = srv.Serve(lis)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	cleanup := func() {
		_ = conn.Close()
		srv.Stop()
		_ = lis.Close()
		_ = store.Close()
	}
	return gokvv1.NewKVServiceClient(conn), cleanup
}

func TestKVServicePutGetDelete(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	putResp, err := client.Put(ctx, &gokvv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.NoError(t, err)
	require.True(t, putResp.GetUpdated())

	getResp, err := client.Get(ctx, &gokvv1.GetRequest{Key: []byte("k")})
	require.NoError(t, err)
	require.Equal(t, []byte("v"), getResp.GetValue())

	deleteResp, err := client.Delete(ctx, &gokvv1.DeleteRequest{Key: []byte("k")})
	require.NoError(t, err)
	require.True(t, deleteResp.GetDeleted())
}

func TestKVServiceStatusCodes(t *testing.T) {
	client, cleanup := newTestClient(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := client.Get(ctx, &gokvv1.GetRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = client.Get(ctx, &gokvv1.GetRequest{Key: []byte("missing")})
	require.Equal(t, codes.NotFound, status.Code(err))
}
