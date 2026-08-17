package grpcapi

import (
	"context"
	"time"

	"github.com/hashicorp/raft"
	pbv1 "github.com/teddymalhan/pallasdb/pb/v1"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// healthPollInterval bounds how long a leadership change elsewhere in the
// cluster stays invisible to this node's health status. raft.LeaderCh only
// fires when *this* node gains or loses leadership, so losing quorum with the
// leader on another node is observed by polling.
const healthPollInterval = 500 * time.Millisecond

// leadershipSource is the slice of *raft.Raft the health watcher needs.
type leadershipSource interface {
	LeaderCh() <-chan bool
	Leader() raft.ServerAddress
}

// newHealthServer registers a health server reporting per-service status.
// The empty service name carries the process-wide status used by generic
// probes; each registered service also gets its own entry so a client can ask
// specifically whether the KV surface is usable.
func newHealthServer(services []string) *health.Server {
	srv := health.NewServer()
	srv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	for _, service := range services {
		srv.SetServingStatus(service, healthpb.HealthCheckResponse_SERVING)
	}
	return srv
}

// watchLeadershipHealth drives KVService's serving status from Raft. A node
// with no known leader cannot serve writes or linearizable reads, so it reports
// NOT_SERVING and load balancers stop sending it traffic. ClusterService stays
// SERVING throughout: it is what a client calls to find the new leader.
func watchLeadershipHealth(ctx context.Context, srv *health.Server, source leadershipSource) {
	set := func(serving bool) {
		status := healthpb.HealthCheckResponse_NOT_SERVING
		if serving {
			status = healthpb.HealthCheckResponse_SERVING
		}
		srv.SetServingStatus(pbv1.KVService_ServiceDesc.ServiceName, status)
		srv.SetServingStatus(pbv1.SQLService_ServiceDesc.ServiceName, status)
	}

	leaderKnown := source.Leader() != ""
	set(leaderKnown)

	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()
	leaderCh := source.LeaderCh()
	for {
		select {
		case <-ctx.Done():
			srv.Shutdown()
			return
		case <-leaderCh:
		case <-ticker.C:
		}
		if known := source.Leader() != ""; known != leaderKnown {
			leaderKnown = known
			set(known)
		}
	}
}
