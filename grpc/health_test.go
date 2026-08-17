package grpcapi

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/raft"
	"github.com/stretchr/testify/require"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// fakeLeadership stands in for *raft.Raft in the health watcher.
type fakeLeadership struct {
	ch     chan bool
	leader chan raft.ServerAddress
	last   raft.ServerAddress
}

func newFakeLeadership(initial raft.ServerAddress) *fakeLeadership {
	f := &fakeLeadership{ch: make(chan bool, 1), leader: make(chan raft.ServerAddress, 8), last: initial}
	return f
}

func (f *fakeLeadership) LeaderCh() <-chan bool { return f.ch }

func (f *fakeLeadership) Leader() raft.ServerAddress {
	for {
		select {
		case addr := <-f.leader:
			f.last = addr
		default:
			return f.last
		}
	}
}

func (f *fakeLeadership) set(addr raft.ServerAddress) {
	f.leader <- addr
	select {
	case f.ch <- addr != "":
	default:
	}
}

func awaitStatus(t *testing.T, srv interface {
	Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error)
}, service string, want healthpb.HealthCheckResponse_ServingStatus,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := srv.Check(context.Background(), &healthpb.HealthCheckRequest{Service: service})
		return err == nil && resp.GetStatus() == want
	}, 5*time.Second, 10*time.Millisecond, "service %q never reached %s", service, want)
}

// A node with no leader cannot serve writes or linearizable reads, so the KV
// surface must report NOT_SERVING while the cluster is electing.
func TestHealthFollowsLeadership(t *testing.T) {
	source := newFakeLeadership("")
	healthSrv := newHealthServer([]string{
		pbv1.KVService_ServiceDesc.ServiceName,
		pbv1.ClusterService_ServiceDesc.ServiceName,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchLeadershipHealth(ctx, healthSrv, source)

	awaitStatus(t, healthSrv, pbv1.KVService_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_NOT_SERVING)

	// ClusterService keeps serving: it is what a client calls to find the
	// leader it should be talking to instead.
	resp, err := healthSrv.Check(ctx, &healthpb.HealthCheckRequest{Service: pbv1.ClusterService_ServiceDesc.ServiceName})
	require.NoError(t, err)
	require.Equal(t, healthpb.HealthCheckResponse_SERVING, resp.GetStatus())

	source.set("10.0.0.4:7001")
	awaitStatus(t, healthSrv, pbv1.KVService_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_SERVING)

	source.set("")
	awaitStatus(t, healthSrv, pbv1.KVService_ServiceDesc.ServiceName, healthpb.HealthCheckResponse_NOT_SERVING)
}

// A stale read is servable by any node and must not touch Raft at all: the nil
// raft here would panic on any consensus call.
func TestStaleReadSkipsRaft(t *testing.T) {
	server := &ClusterKVServer{}
	require.NoError(t, server.readReady(context.Background(), pbv1.Consistency_CONSISTENCY_STALE))
}
