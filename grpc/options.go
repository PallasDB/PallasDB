package grpcapi

import (
	"context"
	"crypto/tls"
	"log/slog"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// Defaults applied by both server constructors. gRPC itself caps received
// messages at 4 MiB and leaves the send size unlimited; we bound both.
const (
	DefaultMaxRecvMsgSize       = 16 << 20
	DefaultMaxSendMsgSize       = 16 << 20
	DefaultMaxConcurrentStreams = 1024

	// DefaultRangeLimit bounds an unqualified Range scan so a single stream
	// cannot pin the KV snapshot forever. Clients resume by re-issuing the
	// scan with start set to the last key received.
	DefaultRangeLimit = 10_000
	// DefaultRangeMaxDuration bounds how long a Range stream may hold its read
	// transaction open.
	DefaultRangeMaxDuration = 30 * time.Second

	defaultKeepaliveMinTime = 20 * time.Second
	defaultKeepalivePing    = 30 * time.Second
	defaultKeepaliveTimeout = 10 * time.Second
)

// Option configures a PallasDB gRPC server.
//
// Option embeds grpc.EmptyServerOption so PallasDB options and raw
// grpc.ServerOption values can be passed in a single variadic list; the
// constructors separate them. An Option handed to grpc.NewServer directly is a
// no-op, which is exactly what the embedding promises.
type Option struct {
	grpc.EmptyServerOption
	apply func(*serverConfig)
}

func newOption(apply func(*serverConfig)) Option { return Option{apply: apply} }

type serverConfig struct {
	ctx                  context.Context
	logger               *slog.Logger
	recovery             bool
	reflection           bool
	creds                credentials.TransportCredentials
	auth                 *AuthConfig
	metrics              *Metrics
	sql                  SQLExecutor
	maxRecvMsgSize       int
	maxSendMsgSize       int
	maxConcurrentStreams uint32
	enforcement          keepalive.EnforcementPolicy
	keepalive            keepalive.ServerParameters
	ranges               rangeLimits
}

func defaultConfig() serverConfig {
	return serverConfig{
		ctx:                  context.Background(),
		recovery:             true,
		maxRecvMsgSize:       DefaultMaxRecvMsgSize,
		maxSendMsgSize:       DefaultMaxSendMsgSize,
		maxConcurrentStreams: DefaultMaxConcurrentStreams,
		enforcement: keepalive.EnforcementPolicy{
			MinTime:             defaultKeepaliveMinTime,
			PermitWithoutStream: true,
		},
		keepalive: keepalive.ServerParameters{
			Time:    defaultKeepalivePing,
			Timeout: defaultKeepaliveTimeout,
		},
		ranges: rangeLimits{limit: DefaultRangeLimit, maxDuration: DefaultRangeMaxDuration},
	}
}

// buildConfig splits PallasDB options out of a mixed option list and returns
// the resolved configuration plus the grpc.ServerOption slice to build with.
func buildConfig(opts []grpc.ServerOption) (serverConfig, []grpc.ServerOption) {
	cfg := defaultConfig()
	passthrough := make([]grpc.ServerOption, 0, len(opts)+8)
	for _, opt := range opts {
		if own, ok := opt.(Option); ok {
			if own.apply != nil {
				own.apply(&cfg)
			}
			continue
		}
		passthrough = append(passthrough, opt)
	}

	unary := make([]grpc.UnaryServerInterceptor, 0, 4)
	stream := make([]grpc.StreamServerInterceptor, 0, 4)
	if cfg.logger != nil {
		unary = append(unary, LoggingUnaryInterceptor(cfg.logger))
		stream = append(stream, LoggingStreamInterceptor(cfg.logger))
	}
	if cfg.metrics != nil {
		unary = append(unary, cfg.metrics.UnaryInterceptor())
		stream = append(stream, cfg.metrics.StreamInterceptor())
	}
	if cfg.auth != nil {
		unary = append(unary, cfg.auth.UnaryInterceptor())
		stream = append(stream, cfg.auth.StreamInterceptor())
	}
	// Recovery goes last so it wraps the handler most tightly: a panic is
	// converted before any outer interceptor observes it, and the logging and
	// metrics interceptors then record the resulting Internal status.
	if cfg.recovery {
		unary = append(unary, RecoveryUnaryInterceptor(cfg.logger))
		stream = append(stream, RecoveryStreamInterceptor(cfg.logger))
	}

	built := []grpc.ServerOption{
		grpc.MaxRecvMsgSize(cfg.maxRecvMsgSize),
		grpc.MaxSendMsgSize(cfg.maxSendMsgSize),
		grpc.MaxConcurrentStreams(cfg.maxConcurrentStreams),
		grpc.KeepaliveEnforcementPolicy(cfg.enforcement),
		grpc.KeepaliveParams(cfg.keepalive),
	}
	if cfg.creds != nil {
		built = append(built, grpc.Creds(cfg.creds))
	}
	if len(unary) > 0 {
		built = append(built, grpc.ChainUnaryInterceptor(unary...))
	}
	if len(stream) > 0 {
		built = append(built, grpc.ChainStreamInterceptor(stream...))
	}
	// Caller-supplied grpc options come last so they win over our defaults.
	return cfg, append(built, passthrough...)
}

// WithContext bounds the lifetime of background goroutines the server starts
// (currently the health watcher). It defaults to context.Background().
func WithContext(ctx context.Context) Option {
	return newOption(func(c *serverConfig) {
		if ctx != nil {
			c.ctx = ctx
		}
	})
}

// WithLogger installs the unary and stream logging interceptors.
func WithLogger(logger *slog.Logger) Option {
	return newOption(func(c *serverConfig) { c.logger = logger })
}

// WithRecovery toggles the panic-recovery interceptors. They are on by default:
// the storage layer asserts with panics on hot paths, and on the Raft apply path
// an unrecovered panic is deterministic across every replica.
func WithRecovery(enabled bool) Option {
	return newOption(func(c *serverConfig) { c.recovery = enabled })
}

// WithReflection enables the gRPC server reflection service so grpcurl and
// evans can introspect the server.
func WithReflection() Option {
	return newOption(func(c *serverConfig) { c.reflection = true })
}

// WithTLS enables server TLS, and mutual TLS when the config asks for it.
//
// A misconfigured TLS setup (unreadable certificate, empty CA bundle, a minimum
// version below TLS 1.2) fails closed: the server starts but rejects every
// handshake with the configuration error. A server asked for TLS never serves
// plaintext.
func WithTLS(cfg TLSConfig) Option {
	creds, err := cfg.Credentials()
	if err != nil {
		creds = failClosedCredentials(err)
	}
	return newOption(func(c *serverConfig) { c.creds = creds })
}

// WithCredentials installs already-built transport credentials, for callers
// that source certificates from somewhere other than the filesystem.
func WithCredentials(creds credentials.TransportCredentials) Option {
	return newOption(func(c *serverConfig) { c.creds = creds })
}

// failClosedCredentials rejects every handshake with err.
func failClosedCredentials(err error) credentials.TransportCredentials {
	return credentials.NewTLS(&tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return nil, err
		},
	})
}

// WithAuthToken gates ClusterService on a shared secret presented as
// "authorization: Bearer <token>".
func WithAuthToken(token string) Option {
	return newOption(func(c *serverConfig) { c.auth = c.auth.withToken(token) })
}

// WithMTLSAuth gates ClusterService on a verified client certificate. An empty
// name list accepts any certificate the client CA vouches for; otherwise the
// certificate's common name or one of its DNS SANs must be listed.
func WithMTLSAuth(allowedNames ...string) Option {
	return newOption(func(c *serverConfig) { c.auth = c.auth.withPeerNames(allowedNames) })
}

// WithMetrics records per-RPC counts, latency and status codes into m.
func WithMetrics(m *Metrics) Option {
	return newOption(func(c *serverConfig) { c.metrics = m })
}

// WithSQL registers SQLService backed by exec.
func WithSQL(exec SQLExecutor) Option {
	return newOption(func(c *serverConfig) { c.sql = exec })
}

// WithMessageSize overrides the receive and send message limits, in bytes.
func WithMessageSize(recv, send int) Option {
	return newOption(func(c *serverConfig) {
		c.maxRecvMsgSize, c.maxSendMsgSize = recv, send
	})
}

// WithMaxConcurrentStreams overrides the per-connection stream limit.
func WithMaxConcurrentStreams(n uint32) Option {
	return newOption(func(c *serverConfig) { c.maxConcurrentStreams = n })
}

// WithKeepalivePolicy overrides the keepalive enforcement policy and the
// server's own keepalive parameters.
func WithKeepalivePolicy(enforcement keepalive.EnforcementPolicy, params keepalive.ServerParameters) Option {
	return newOption(func(c *serverConfig) {
		c.enforcement, c.keepalive = enforcement, params
	})
}

// WithRangeBounds caps how many rows a single Range stream returns and how long
// it may hold its read transaction. A zero limit or duration means unbounded.
func WithRangeBounds(limit uint64, maxDuration time.Duration) Option {
	return newOption(func(c *serverConfig) {
		c.ranges = rangeLimits{limit: limit, maxDuration: maxDuration}
	})
}
