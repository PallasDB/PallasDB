package grpcapi

import (
	"context"
	"fmt"
	"strings"
	"time"

	gometrics "github.com/armon/go-metrics"
	gometricsprom "github.com/armon/go-metrics/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

// Metrics collects per-RPC counters and latency histograms into a Prometheus
// registry the CLI can expose over HTTP. CollectRaftMetrics folds Raft's own
// go-metrics output into the same registry.
type Metrics struct {
	registry *prometheus.Registry
	started  *prometheus.CounterVec
	handled  *prometheus.CounterVec
	latency  *prometheus.HistogramVec
	inFlight *prometheus.GaugeVec
}

// NewMetrics builds a Metrics backed by its own registry, pre-populated with
// the process and Go runtime collectors.
func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		collectors.NewGoCollector(),
	)
	m := NewMetricsWithRegistry(registry)
	return m
}

// NewMetricsWithRegistry registers the RPC collectors into an existing
// registry, so a process that already exports metrics keeps one endpoint.
func NewMetricsWithRegistry(registry *prometheus.Registry) *Metrics {
	m := &Metrics{
		registry: registry,
		started: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "pallasdb",
			Subsystem: "grpc",
			Name:      "requests_started_total",
			Help:      "Total RPCs started on the server.",
		}, []string{"grpc_service", "grpc_method", "grpc_type"}),
		handled: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "pallasdb",
			Subsystem: "grpc",
			Name:      "requests_handled_total",
			Help:      "Total RPCs completed on the server, by status code.",
		}, []string{"grpc_service", "grpc_method", "grpc_type", "grpc_code"}),
		latency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "pallasdb",
			Subsystem: "grpc",
			Name:      "request_duration_seconds",
			Help:      "Server-side RPC latency.",
			Buckets:   prometheus.ExponentialBuckets(0.0005, 2, 14),
		}, []string{"grpc_service", "grpc_method", "grpc_type"}),
		inFlight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "pallasdb",
			Subsystem: "grpc",
			Name:      "requests_in_flight",
			Help:      "RPCs currently being served.",
		}, []string{"grpc_service", "grpc_method", "grpc_type"}),
	}
	registry.MustRegister(m.started, m.handled, m.latency, m.inFlight)
	return m
}

// Registry exposes the underlying registry so the CLI can serve /metrics.
func (m *Metrics) Registry() *prometheus.Registry { return m.registry }

// Gatherer exposes the registry as a prometheus.Gatherer.
func (m *Metrics) Gatherer() prometheus.Gatherer { return m.registry }

// CollectRaftMetrics routes the process-wide go-metrics stream into this
// registry. Raft has always emitted these counters and timers; until now
// nothing collected them, so replication latency and log throughput were
// invisible.
//
// go-metrics has a single global sink, so this installs one and must be called
// at most once per process.
func (m *Metrics) CollectRaftMetrics(serviceName string) error {
	sink, err := gometricsprom.NewPrometheusSinkFrom(gometricsprom.PrometheusOpts{
		Registerer: m.registry,
		Expiration: raftMetricExpiration,
	})
	if err != nil {
		return fmt.Errorf("build raft metrics sink: %w", err)
	}
	cfg := gometrics.DefaultConfig(serviceName)
	// The hostname is a label the scrape target already provides.
	cfg.EnableHostname = false
	if _, err := gometrics.NewGlobal(cfg, sink); err != nil {
		return fmt.Errorf("install raft metrics sink: %w", err)
	}
	return nil
}

// raftMetricExpiration drops series for operations that stopped happening, so
// a leader that stepped down does not report stale replication timings.
const raftMetricExpiration = 5 * time.Minute

// UnaryInterceptor records unary RPCs.
func (m *Metrics) UnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		done := m.begin(info.FullMethod, "unary")
		resp, err := handler(ctx, req)
		done(err)
		return resp, err
	}
}

// StreamInterceptor records streaming RPCs; without it Range and Query would be
// invisible.
func (m *Metrics) StreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		done := m.begin(info.FullMethod, streamType(info))
		err := handler(srv, ss)
		done(err)
		return err
	}
}

func (m *Metrics) begin(fullMethod, rpcType string) func(error) {
	service, method := splitMethod(fullMethod)
	labels := prometheus.Labels{"grpc_service": service, "grpc_method": method, "grpc_type": rpcType}
	m.started.With(labels).Inc()
	m.inFlight.With(labels).Inc()
	start := time.Now()
	return func(err error) {
		m.inFlight.With(labels).Dec()
		m.latency.With(labels).Observe(time.Since(start).Seconds())
		m.handled.With(prometheus.Labels{
			"grpc_service": service,
			"grpc_method":  method,
			"grpc_type":    rpcType,
			"grpc_code":    status.Code(err).String(),
		}).Inc()
	}
}

func streamType(info *grpc.StreamServerInfo) string {
	switch {
	case info.IsServerStream && info.IsClientStream:
		return "bidi_stream"
	case info.IsClientStream:
		return "client_stream"
	default:
		return "server_stream"
	}
}

// splitMethod turns "/pallasdb.v1.KVService/Get" into its service and method.
func splitMethod(fullMethod string) (service, method string) {
	trimmed := strings.TrimPrefix(fullMethod, "/")
	if service, method, ok := strings.Cut(trimmed, "/"); ok {
		return service, method
	}
	return trimmed, ""
}
