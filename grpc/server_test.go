package grpcapi

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/teddymalhan/pallasdb/db"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	reflectpb "google.golang.org/grpc/reflection/grpc_reflection_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

// newTestServer starts a single-node server on an in-memory listener and
// returns the store it serves plus a dialer for it. Everything is torn down
// through t.Cleanup.
func newTestServer(t *testing.T, opts ...grpc.ServerOption) (store *db.KV, dial func(...grpc.DialOption) *grpc.ClientConn, srv *grpc.Server) {
	t.Helper()

	store, err := db.NewKV(t.TempDir())
	require.NoError(t, err)

	lis := bufconn.Listen(1024 * 1024)
	srv = NewGRPCServer(store, opts...)
	go func() { _ = srv.Serve(lis) }()

	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
		_ = store.Close()
	})

	dial = func(dialOpts ...grpc.DialOption) *grpc.ClientConn {
		t.Helper()
		dialOpts = append(dialOpts,
			grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		)
		conn, err := grpc.NewClient("passthrough:///bufnet", dialOpts...)
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	return store, dial, srv
}

// newTestClient is the plaintext KV client used by most tests.
func newTestClient(t *testing.T, opts ...grpc.ServerOption) (client pbv1.KVServiceClient, store *db.KV) {
	t.Helper()
	store, dial, _ := newTestServer(t, opts...)
	return pbv1.NewKVServiceClient(dial(grpc.WithTransportCredentials(insecure.NewCredentials()))), store
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestKVServicePutGetDelete(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := testContext(t)

	putResp, err := client.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.NoError(t, err)
	require.True(t, putResp.GetUpdated())

	getResp, err := client.Get(ctx, &pbv1.GetRequest{Key: []byte("k")})
	require.NoError(t, err)
	require.Equal(t, []byte("v"), getResp.GetValue())

	deleteResp, err := client.Delete(ctx, &pbv1.DeleteRequest{Key: []byte("k")})
	require.NoError(t, err)
	require.True(t, deleteResp.GetDeleted())
}

func TestKVServiceStatusCodes(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := testContext(t)

	_, err := client.Get(ctx, &pbv1.GetRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = client.Get(ctx, &pbv1.GetRequest{Key: []byte("missing")})
	require.Equal(t, codes.NotFound, status.Code(err))
}

// A delete must be safe to retry: the second attempt reports that there was
// nothing to remove rather than failing the caller.
func TestDeleteMissingKeyIsIdempotent(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := testContext(t)

	resp, err := client.Delete(ctx, &pbv1.DeleteRequest{Key: []byte("absent")})
	require.NoError(t, err)
	require.False(t, resp.GetDeleted())

	_, err = client.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.NoError(t, err)

	first, err := client.Delete(ctx, &pbv1.DeleteRequest{Key: []byte("k")})
	require.NoError(t, err)
	require.True(t, first.GetDeleted())

	retry, err := client.Delete(ctx, &pbv1.DeleteRequest{Key: []byte("k")})
	require.NoError(t, err)
	require.False(t, retry.GetDeleted())
}

func collectRange(t *testing.T, client pbv1.KVServiceClient, ctx context.Context, req *pbv1.RangeRequest) []*pbv1.RangeResponse {
	t.Helper()
	stream, err := client.Range(ctx, req)
	require.NoError(t, err)
	var out []*pbv1.RangeResponse
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			return out
		}
		require.NoError(t, err)
		out = append(out, msg)
	}
}

func rangeKeys(msgs []*pbv1.RangeResponse) []string {
	keys := make([]string, len(msgs))
	for i, msg := range msgs {
		keys[i] = string(msg.GetKey())
	}
	return keys
}

func TestRangeScans(t *testing.T) {
	client, store := newTestClient(t)
	ctx := testContext(t)

	for _, key := range []string{"a", "b", "c", "d"} {
		_, err := store.SetEx([]byte(key), []byte("v"+key), db.ModeUpsert)
		require.NoError(t, err)
	}

	t.Run("whole keyspace with empty bounds", func(t *testing.T) {
		msgs := collectRange(t, client, ctx, &pbv1.RangeRequest{})
		require.Equal(t, []string{"a", "b", "c", "d"}, rangeKeys(msgs))
		require.Equal(t, []byte("va"), msgs[0].GetValue())
	})

	t.Run("empty stop scans to the last key", func(t *testing.T) {
		msgs := collectRange(t, client, ctx, &pbv1.RangeRequest{Start: []byte("b")})
		require.Equal(t, []string{"b", "c", "d"}, rangeKeys(msgs))
	})

	t.Run("empty start scans from the first key", func(t *testing.T) {
		msgs := collectRange(t, client, ctx, &pbv1.RangeRequest{Stop: []byte("b")})
		require.Equal(t, []string{"a", "b"}, rangeKeys(msgs))
	})

	t.Run("limit truncates", func(t *testing.T) {
		msgs := collectRange(t, client, ctx, &pbv1.RangeRequest{Limit: 2})
		require.Equal(t, []string{"a", "b"}, rangeKeys(msgs))
	})

	t.Run("keys only omits values", func(t *testing.T) {
		msgs := collectRange(t, client, ctx, &pbv1.RangeRequest{KeysOnly: true})
		require.Equal(t, []string{"a", "b", "c", "d"}, rangeKeys(msgs))
		for _, msg := range msgs {
			require.Empty(t, msg.GetValue())
		}
	})

	t.Run("descending from a start key", func(t *testing.T) {
		msgs := collectRange(t, client, ctx, &pbv1.RangeRequest{Start: []byte("d"), Descending: true})
		require.Equal(t, []string{"d", "c", "b", "a"}, rangeKeys(msgs))
	})
}

func TestRangeRespectsServerLimit(t *testing.T) {
	client, store := newTestClient(t, WithRangeBounds(2, time.Minute))
	ctx := testContext(t)
	for _, key := range []string{"a", "b", "c", "d"} {
		_, err := store.SetEx([]byte(key), []byte(key), db.ModeUpsert)
		require.NoError(t, err)
	}

	msgs := collectRange(t, client, ctx, &pbv1.RangeRequest{Limit: 100})
	require.Equal(t, []string{"a", "b"}, rangeKeys(msgs))
}

// A descending scan of the whole keyspace needs a seek-to-end the storage layer
// does not expose yet; it must be refused clearly rather than silently reversed.
func TestRangeDescendingWithoutStartIsRejected(t *testing.T) {
	client, _ := newTestClient(t)
	ctx := testContext(t)

	stream, err := client.Range(ctx, &pbv1.RangeRequest{Descending: true})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestHealthReportsEachService(t *testing.T) {
	_, dial, _ := newTestServer(t, WithSQL(staticExecutor{}))
	ctx := testContext(t)
	client := healthpb.NewHealthClient(dial(grpc.WithTransportCredentials(insecure.NewCredentials())))

	for _, service := range []string{"", pbv1.KVService_ServiceDesc.ServiceName, pbv1.SQLService_ServiceDesc.ServiceName} {
		resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
		require.NoError(t, err, "service %q", service)
		require.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus(), "service %q", service)
	}

	_, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: pbv1.ClusterService_ServiceDesc.ServiceName})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestTLSHandshake(t *testing.T) {
	pki := newTestPKI(t)

	t.Run("mutual TLS requires a client certificate", func(t *testing.T) {
		_, dial, _ := newTestServer(t, WithTLS(TLSConfig{
			CertFile:          pki.serverCertFile,
			KeyFile:           pki.serverKeyFile,
			ClientCAFile:      pki.caFile,
			RequireClientCert: true,
		}))
		ctx := testContext(t)

		clientCert, err := tls.LoadX509KeyPair(pki.clientCertFile, pki.clientKeyFile)
		require.NoError(t, err)

		withCert := pbv1.NewKVServiceClient(dial(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			RootCAs:      pki.caPool,
			Certificates: []tls.Certificate{clientCert},
			ServerName:   "localhost",
			MinVersion:   tls.VersionTLS12,
		}))))
		_, err = withCert.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
		require.NoError(t, err)

		withoutCert := pbv1.NewKVServiceClient(dial(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			RootCAs:    pki.caPool,
			ServerName: "localhost",
			MinVersion: tls.VersionTLS12,
		}))))
		_, err = withoutCert.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
		require.Error(t, err)
		require.Equal(t, codes.Unavailable, status.Code(err))
	})

	t.Run("server TLS without client certificates", func(t *testing.T) {
		_, dial, _ := newTestServer(t, WithTLS(TLSConfig{
			CertFile: pki.serverCertFile,
			KeyFile:  pki.serverKeyFile,
		}))
		ctx := testContext(t)

		client := pbv1.NewKVServiceClient(dial(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			RootCAs:    pki.caPool,
			ServerName: "localhost",
			MinVersion: tls.VersionTLS12,
		}))))
		_, err := client.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
		require.NoError(t, err)

		plaintext := pbv1.NewKVServiceClient(dial(grpc.WithTransportCredentials(insecure.NewCredentials())))
		_, err = plaintext.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
		require.Error(t, err)
	})
}

// A TLS configuration that cannot be loaded must never degrade to plaintext.
func TestTLSMisconfigurationFailsClosed(t *testing.T) {
	_, dial, _ := newTestServer(t, WithTLS(TLSConfig{
		CertFile: "/nonexistent/server.pem",
		KeyFile:  "/nonexistent/server.key",
	}))
	ctx := testContext(t)

	client := pbv1.NewKVServiceClient(dial(grpc.WithTransportCredentials(insecure.NewCredentials())))
	_, err := client.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.Error(t, err)
}

func TestTLSConfigValidation(t *testing.T) {
	pki := newTestPKI(t)

	_, err := TLSConfig{KeyFile: pki.serverKeyFile}.Credentials()
	require.ErrorContains(t, err, "cert file and key file are required")

	_, err = TLSConfig{
		CertFile:   pki.serverCertFile,
		KeyFile:    pki.serverKeyFile,
		MinVersion: tls.VersionTLS11,
	}.Credentials()
	require.ErrorContains(t, err, "below TLS 1.2")

	_, err = TLSConfig{
		CertFile:          pki.serverCertFile,
		KeyFile:           pki.serverKeyFile,
		RequireClientCert: true,
	}.Credentials()
	require.ErrorContains(t, err, "client CA file is required")

	creds, err := TLSConfig{CertFile: pki.serverCertFile, KeyFile: pki.serverKeyFile}.Credentials()
	require.NoError(t, err)
	require.NotNil(t, creds)
}

// Membership RPCs must be gated when a shared secret is configured, and only
// membership RPCs: the data plane keeps working untouched.
func TestAuthGatesClusterService(t *testing.T) {
	const token = "s3cret"
	store, dial, srv := newTestServer(t, WithAuthToken(token))
	_ = store
	pbv1.RegisterClusterServiceServer(srv, pbv1.UnimplementedClusterServiceServer{})

	ctx := testContext(t)
	conn := dial(grpc.WithTransportCredentials(insecure.NewCredentials()))
	cluster := pbv1.NewClusterServiceClient(conn)

	_, err := cluster.Join(ctx, &pbv1.JoinRequest{NodeId: "n2", RaftAddr: "10.0.0.4:7001"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	wrong := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer nope")
	_, err = cluster.Join(wrong, &pbv1.JoinRequest{NodeId: "n2", RaftAddr: "10.0.0.4:7001"})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	// The handler is the unimplemented stub, so reaching it proves the request
	// passed the gate.
	authed := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
	_, err = cluster.Join(authed, &pbv1.JoinRequest{NodeId: "n2", RaftAddr: "10.0.0.4:7001"})
	require.Equal(t, codes.Unimplemented, status.Code(err))

	kv := pbv1.NewKVServiceClient(conn)
	_, err = kv.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.NoError(t, err)
}

func TestAuthRequiresPeerCertificate(t *testing.T) {
	pki := newTestPKI(t)
	_, dial, srv := newTestServer(t,
		WithTLS(TLSConfig{
			CertFile:     pki.serverCertFile,
			KeyFile:      pki.serverKeyFile,
			ClientCAFile: pki.caFile,
		}),
		WithMTLSAuth("pallasdb-test-client"),
	)
	pbv1.RegisterClusterServiceServer(srv, pbv1.UnimplementedClusterServiceServer{})
	ctx := testContext(t)

	anonymous := pbv1.NewClusterServiceClient(dial(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs:    pki.caPool,
		ServerName: "localhost",
		MinVersion: tls.VersionTLS12,
	}))))
	_, err := anonymous.GetLeader(ctx, &pbv1.GetLeaderRequest{})
	require.Equal(t, codes.Unauthenticated, status.Code(err))

	clientCert, err := tls.LoadX509KeyPair(pki.clientCertFile, pki.clientKeyFile)
	require.NoError(t, err)
	identified := pbv1.NewClusterServiceClient(dial(grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
		RootCAs:      pki.caPool,
		Certificates: []tls.Certificate{clientCert},
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
	}))))
	_, err = identified.GetLeader(ctx, &pbv1.GetLeaderRequest{})
	require.Equal(t, codes.Unimplemented, status.Code(err))
}

func TestMetricsRecordRPCs(t *testing.T) {
	metrics := NewMetrics()
	client, _ := newTestClient(t, WithMetrics(metrics))
	ctx := testContext(t)

	_, err := client.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.NoError(t, err)
	_, err = client.Get(ctx, &pbv1.GetRequest{Key: []byte("missing")})
	require.Equal(t, codes.NotFound, status.Code(err))
	collectRange(t, client, ctx, &pbv1.RangeRequest{})

	families, err := metrics.Registry().Gather()
	require.NoError(t, err)

	handled := map[string]float64{}
	for _, family := range families {
		if family.GetName() != "pallasdb_grpc_requests_handled_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			key := ""
			for _, label := range metric.GetLabel() {
				if label.GetName() == "grpc_method" || label.GetName() == "grpc_code" {
					key += label.GetValue() + "/"
				}
			}
			handled[key] += metric.GetCounter().GetValue()
		}
	}
	require.Equal(t, float64(1), handled["OK/Put/"], "handled: %v", handled)
	require.Equal(t, float64(1), handled["NotFound/Get/"], "handled: %v", handled)
	require.Equal(t, float64(1), handled["OK/Range/"], "handled: %v", handled)
}

// Reflection is what lets grpcurl and evans talk to the server without a copy
// of the schema, and it stays opt-in.
func TestReflectionIsOptIn(t *testing.T) {
	ctx := testContext(t)

	_, dialOff, _ := newTestServer(t)
	off := reflectpb.NewServerReflectionClient(dialOff(grpc.WithTransportCredentials(insecure.NewCredentials())))
	stream, err := off.ServerReflectionInfo(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&reflectpb.ServerReflectionRequest{
		MessageRequest: &reflectpb.ServerReflectionRequest_ListServices{},
	}))
	_, err = stream.Recv()
	require.Equal(t, codes.Unimplemented, status.Code(err))

	_, dialOn, _ := newTestServer(t, WithReflection())
	on := reflectpb.NewServerReflectionClient(dialOn(grpc.WithTransportCredentials(insecure.NewCredentials())))
	stream, err = on.ServerReflectionInfo(ctx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&reflectpb.ServerReflectionRequest{
		MessageRequest: &reflectpb.ServerReflectionRequest_ListServices{},
	}))
	resp, err := stream.Recv()
	require.NoError(t, err)

	names := make([]string, 0, 4)
	for _, service := range resp.GetListServicesResponse().GetService() {
		names = append(names, service.GetName())
	}
	require.Contains(t, names, pbv1.KVService_ServiceDesc.ServiceName)
}

// The send limit was previously unlimited; both directions are now bounded.
func TestMessageSizeLimits(t *testing.T) {
	client, store := newTestClient(t, WithMessageSize(1024, 1024))
	ctx := testContext(t)

	oversized := make([]byte, 4096)
	_, err := client.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: oversized})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))

	// A value that only the response exceeds the limit with is caught on the
	// way out rather than streamed unbounded.
	_, err = store.SetEx([]byte("big"), oversized, db.ModeUpsert)
	require.NoError(t, err)
	_, err = client.Get(ctx, &pbv1.GetRequest{Key: []byte("big")})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
}

// The constructors take PallasDB options and raw grpc.ServerOption values in
// one list; the CLI passes the latter today.
func TestMixedOptionLists(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client, _ := newTestClient(t,
		grpc.ChainUnaryInterceptor(LoggingUnaryInterceptor(logger)),
		WithLogger(logger),
		WithReflection(),
	)
	ctx := testContext(t)

	_, err := client.Put(ctx, &pbv1.PutRequest{Key: []byte("k"), Value: []byte("v")})
	require.NoError(t, err)

	resp, err := client.Get(ctx, &pbv1.GetRequest{Key: []byte("k")})
	require.NoError(t, err)
	require.Equal(t, []byte("v"), resp.GetValue())
}
