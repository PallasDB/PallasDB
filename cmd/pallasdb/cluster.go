package main

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/teddymalhan/pallasdb/cluster"
	"github.com/teddymalhan/pallasdb/db"
	grpcapi "github.com/teddymalhan/pallasdb/grpc"
	"google.golang.org/grpc"
)

type clusterStartOptions struct {
	grpcAddr     string
	dataDir      string
	raftAddr     string
	raftDir      string
	nodeID       string
	joinAddr     string
	applyTimeout time.Duration

	bootstrapExpect    int
	autoCompact        bool
	nonVoter           bool
	leaveOnShutdown    bool
	failureGracePeriod time.Duration

	serfEnabled       bool
	serfAddr          string
	serfAdvertiseAddr string
	serfJoinAddrs     []string
	serfEventBuffer   int
	serfEncryptKey    string

	tlsCertFile          string
	tlsKeyFile           string
	tlsClientCAFile      string
	tlsRequireClientCert bool
	tlsMinVersion        string
}

func newClusterCommand(root *rootOptions, config *configOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Run and manage a Raft-backed PallasDB cluster",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newClusterStartCommand(root, config))
	return cmd
}

func newClusterStartCommand(root *rootOptions, config *configOptions) *cobra.Command {
	opts := &clusterStartOptions{}
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start a Raft-backed PallasDB node",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := validateClusterStartOptions(root, opts); err != nil {
				return err
			}
			logger, err := newLogger(root.logFormat)
			if err != nil {
				return err
			}

			store, err := db.NewKV(opts.dataDir, clusterKVOptions(opts, config.viper)...)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}

			encryptKey, err := decodeSerfEncryptKey(opts.serfEncryptKey)
			if err != nil {
				_ = store.Close()
				return err
			}
			tlsCfg, err := clusterTLSConfig(opts)
			if err != nil {
				_ = store.Close()
				return err
			}

			nodeCfg := cluster.Config{
				NodeID:    opts.nodeID,
				GRPCAddr:  opts.grpcAddr,
				RaftAddr:  opts.raftAddr,
				RaftDir:   opts.raftDir,
				Bootstrap: shouldBootstrapCluster(opts),
				JoinAddr:  opts.joinAddr,
				Timeout:   opts.applyTimeout,

				BootstrapExpect:    bootstrapExpectCount(opts),
				NonVoter:           opts.nonVoter,
				LeaveOnShutdown:    opts.leaveOnShutdown,
				FailureGracePeriod: opts.failureGracePeriod,

				RaftTLS: tlsCfg,
				JoinTLS: tlsCfg,

				SerfEnabled:       opts.serfEnabled,
				SerfAddr:          opts.serfAddr,
				SerfAdvertiseAddr: opts.serfAdvertiseAddr,
				SerfJoinAddrs:     opts.serfJoinAddrs,
				SerfEventBuffer:   opts.serfEventBuffer,
				SerfEncryptKey:    encryptKey,
			}

			node, err := cluster.Open(store, nodeCfg)
			if err != nil {
				_ = store.Close()
				return fmt.Errorf("open cluster node: %w", err)
			}

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			listenConfig := net.ListenConfig{}
			lis, err := listenConfig.Listen(ctx, "tcp", opts.grpcAddr)
			if err != nil {
				return shutdownClusterNode(node, fmt.Errorf("listen on %s: %w", opts.grpcAddr, err))
			}

			srv := grpcapi.NewClusterGRPCServer(
				node,
				opts.applyTimeout,
				grpc.ChainUnaryInterceptor(grpcapi.LoggingUnaryInterceptor(logger)),
			)

			logger.Info("starting cluster node",
				"grpc_addr", opts.grpcAddr,
				"raft_addr", opts.raftAddr,
				"node_id", opts.nodeID,
				"data_dir", opts.dataDir,
				"raft_dir", opts.raftDir,
				"serf_enabled", opts.serfEnabled,
				"serf_addr", opts.serfAddr,
				"bootstrap_expect", opts.bootstrapExpect,
				"non_voter", opts.nonVoter,
				"auto_compact", opts.autoCompact,
				"tls", opts.tlsCertFile != "",
			)

			runErr := grpcapi.ServeWithGracefulStopTimeout(ctx, lis, srv, root.shutdownTimeout)
			if errors.Is(runErr, context.Canceled) {
				runErr = nil
			}
			if err := shutdownClusterNode(node, runErr); err != nil {
				return err
			}
			logger.Info("cluster node stopped")
			return nil
		},
	}

	cmd.Flags().StringVar(&opts.grpcAddr, "grpc-addr", ":50051", "gRPC listen address")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "data", "KV database directory")
	cmd.Flags().StringVar(&opts.raftAddr, "raft-addr", ":7001", "Raft TCP transport address")
	cmd.Flags().StringVar(&opts.raftDir, "raft-dir", "raft", "Raft state directory")
	cmd.Flags().StringVar(&opts.nodeID, "node-id", "", "unique node ID within the cluster")
	cmd.Flags().StringVar(&opts.joinAddr, "join", "", "gRPC address of an existing node to join; empty to bootstrap")
	cmd.Flags().DurationVar(&opts.applyTimeout, "apply-timeout", 10*time.Second, "Raft apply/barrier timeout")
	cmd.Flags().IntVar(&opts.bootstrapExpect, "bootstrap-expect", 1, "number of nodes to wait for before one of them bootstraps the cluster; 1 bootstraps a single-node cluster immediately")
	cmd.Flags().BoolVar(&opts.autoCompact, "auto-compact", true, "compact the LSM tree in the background as Raft entries are applied")
	cmd.Flags().BoolVar(&opts.nonVoter, "non-voter", false, "join as a read-only replica that never becomes a voter")
	cmd.Flags().BoolVar(&opts.leaveOnShutdown, "leave-on-shutdown", true, "remove this node from the Raft configuration on shutdown")
	cmd.Flags().DurationVar(&opts.failureGracePeriod, "failure-grace-period", 30*time.Second, "how long a gossip-failed node may stay unreachable before the leader evicts it; negative disables eviction")
	cmd.Flags().BoolVar(&opts.serfEnabled, "serf-enabled", true, "enable Serf gossip discovery")
	cmd.Flags().StringVar(&opts.serfAddr, "serf-addr", ":7946", "Serf gossip bind address")
	cmd.Flags().StringVar(&opts.serfAdvertiseAddr, "serf-advertise-addr", "", "Serf advertise address")
	cmd.Flags().StringSliceVar(&opts.serfJoinAddrs, "serf-join", nil, "Serf addresses of existing nodes")
	cmd.Flags().IntVar(&opts.serfEventBuffer, "serf-event-buffer", 64, "Serf event channel buffer size")
	cmd.Flags().StringVar(&opts.serfEncryptKey, "serf-encrypt-key", "", "base64-encoded 16/24/32-byte Serf gossip encryption key")
	cmd.Flags().StringVar(&opts.tlsCertFile, "tls-cert-file", "", "PEM certificate for the Raft transport and node-to-node client")
	cmd.Flags().StringVar(&opts.tlsKeyFile, "tls-key-file", "", "PEM private key matching --tls-cert-file")
	cmd.Flags().StringVar(&opts.tlsClientCAFile, "tls-client-ca-file", "", "PEM CA bundle used to verify peer certificates")
	cmd.Flags().BoolVar(&opts.tlsRequireClientCert, "tls-require-client-cert", false, "require and verify peer certificates (mTLS)")
	cmd.Flags().StringVar(&opts.tlsMinVersion, "tls-min-version", "1.2", "minimum TLS version: 1.2 or 1.3")
	cmd.Flags().Bool("cache-enabled", false, "enable in-process read cache")
	cmd.Flags().Int64("cache-max-cost", 32*1024*1024, "max cache bytes")
	cmd.Flags().Int64("cache-num-counters", 1_000_000, "cache num counters (≈10× expected item count)")
	config.bindFlag(cmd.Flags(), "grpc-addr", "cluster.grpc_addr")
	config.bindFlag(cmd.Flags(), "data-dir", "cluster.data_dir")
	config.bindFlag(cmd.Flags(), "raft-addr", "cluster.raft_addr")
	config.bindFlag(cmd.Flags(), "raft-dir", "cluster.raft_dir")
	config.bindFlag(cmd.Flags(), "node-id", "cluster.node_id")
	config.bindFlag(cmd.Flags(), "join", "cluster.join")
	config.bindFlag(cmd.Flags(), "apply-timeout", "cluster.apply_timeout")
	config.bindFlag(cmd.Flags(), "bootstrap-expect", "cluster.bootstrap_expect")
	config.bindFlag(cmd.Flags(), "auto-compact", "cluster.auto_compact")
	config.bindFlag(cmd.Flags(), "non-voter", "cluster.non_voter")
	config.bindFlag(cmd.Flags(), "leave-on-shutdown", "cluster.leave_on_shutdown")
	config.bindFlag(cmd.Flags(), "failure-grace-period", "cluster.failure_grace_period")
	config.bindFlag(cmd.Flags(), "serf-enabled", "cluster.serf.enabled")
	config.bindFlag(cmd.Flags(), "serf-addr", "cluster.serf.addr")
	config.bindFlag(cmd.Flags(), "serf-advertise-addr", "cluster.serf.advertise_addr")
	config.bindFlag(cmd.Flags(), "serf-join", "cluster.serf.join")
	config.bindFlag(cmd.Flags(), "serf-event-buffer", "cluster.serf.event_buffer")
	config.bindFlag(cmd.Flags(), "serf-encrypt-key", "cluster.serf.encrypt_key")
	config.bindFlag(cmd.Flags(), "tls-cert-file", "cluster.tls.cert_file")
	config.bindFlag(cmd.Flags(), "tls-key-file", "cluster.tls.key_file")
	config.bindFlag(cmd.Flags(), "tls-client-ca-file", "cluster.tls.client_ca_file")
	config.bindFlag(cmd.Flags(), "tls-require-client-cert", "cluster.tls.require_client_cert")
	config.bindFlag(cmd.Flags(), "tls-min-version", "cluster.tls.min_version")
	config.bindFlag(cmd.Flags(), "cache-enabled", "cache.enabled")
	config.bindFlag(cmd.Flags(), "cache-max-cost", "cache.max_cost_bytes")
	config.bindFlag(cmd.Flags(), "cache-num-counters", "cache.num_counters")
	config.registerApply(func(v *viper.Viper) {
		opts.grpcAddr = v.GetString("cluster.grpc_addr")
		opts.dataDir = v.GetString("cluster.data_dir")
		opts.raftAddr = v.GetString("cluster.raft_addr")
		opts.raftDir = v.GetString("cluster.raft_dir")
		opts.nodeID = v.GetString("cluster.node_id")
		opts.joinAddr = v.GetString("cluster.join")
		opts.applyTimeout = v.GetDuration("cluster.apply_timeout")
		opts.bootstrapExpect = v.GetInt("cluster.bootstrap_expect")
		opts.nonVoter = v.GetBool("cluster.non_voter")
		opts.leaveOnShutdown = v.GetBool("cluster.leave_on_shutdown")
		opts.failureGracePeriod = v.GetDuration("cluster.failure_grace_period")
		opts.serfEnabled = v.GetBool("cluster.serf.enabled")
		opts.serfAddr = v.GetString("cluster.serf.addr")
		opts.serfAdvertiseAddr = v.GetString("cluster.serf.advertise_addr")
		opts.serfJoinAddrs = v.GetStringSlice("cluster.serf.join")
		opts.serfEventBuffer = v.GetInt("cluster.serf.event_buffer")
		opts.serfEncryptKey = v.GetString("cluster.serf.encrypt_key")
		opts.tlsCertFile = v.GetString("cluster.tls.cert_file")
		opts.tlsKeyFile = v.GetString("cluster.tls.key_file")
		opts.tlsClientCAFile = v.GetString("cluster.tls.client_ca_file")
		opts.tlsRequireClientCert = v.GetBool("cluster.tls.require_client_cert")
		opts.tlsMinVersion = v.GetString("cluster.tls.min_version")
	})
	return cmd
}

// shouldBootstrapCluster reports whether this node forms a fresh single-node
// cluster on its own. It deliberately never returns true once the node has been
// told about peers: bootstrapping while other nodes are reachable is exactly
// how a symmetric start turns into N single-node clusters that each accept
// writes. Multi-node formation goes through --bootstrap-expect instead.
// clusterKVOptions builds the storage options for a cluster node. Every Raft
// entry lands in the KV write-ahead log, so a cluster node that never compacts
// grows without bound; compaction is on unless the operator turns it off.
func clusterKVOptions(opts *clusterStartOptions, v *viper.Viper) []db.KVOption {
	return append([]db.KVOption{db.WithAutoCompact(opts.autoCompact)}, kvOptsFromViper(v)...)
}

func shouldBootstrapCluster(opts *clusterStartOptions) bool {
	if opts.joinAddr != "" {
		return false
	}
	if opts.nonVoter {
		return false
	}
	if opts.bootstrapExpect >= 2 {
		return false
	}
	if opts.serfEnabled && len(opts.serfJoinAddrs) > 0 {
		return false
	}
	return true
}

// bootstrapExpectCount is the quorum size handed to cluster.Config. It is zero
// unless the operator asked for coordinated multi-node bootstrap.
func bootstrapExpectCount(opts *clusterStartOptions) int {
	if opts.joinAddr != "" || opts.bootstrapExpect < 2 {
		return 0
	}
	return opts.bootstrapExpect
}

func validateClusterStartOptions(root *rootOptions, opts *clusterStartOptions) error {
	return joinErrors(
		requireNonEmptyFlag("node-id", opts.nodeID),
		requirePositiveDuration("--apply-timeout", opts.applyTimeout),
		requirePositiveDuration("--shutdown-timeout", root.shutdownTimeout),
		requirePositiveInt("--serf-event-buffer", opts.serfEventBuffer),
		validateBootstrapExpect(opts),
	)
}

func validateBootstrapExpect(opts *clusterStartOptions) error {
	if opts.bootstrapExpect < 1 {
		return fmt.Errorf("--bootstrap-expect must be at least 1, got %d", opts.bootstrapExpect)
	}
	if opts.bootstrapExpect >= 2 && !opts.serfEnabled {
		return errors.New("--bootstrap-expect requires --serf-enabled")
	}
	if opts.bootstrapExpect >= 2 && opts.joinAddr != "" {
		return errors.New("--bootstrap-expect and --join are mutually exclusive")
	}
	return nil
}

// decodeSerfEncryptKey turns the base64 CLI flag into a memberlist secret key.
func decodeSerfEncryptKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("--serf-encrypt-key must be base64: %w", err)
	}
	switch len(key) {
	case 16, 24, 32:
		return key, nil
	default:
		return nil, fmt.Errorf("--serf-encrypt-key must decode to 16, 24, or 32 bytes, got %d", len(key))
	}
}

// clusterTLSConfig builds the shared TLS settings for the Raft transport and
// the node-to-node gRPC client. Nil means plaintext.
func clusterTLSConfig(opts *clusterStartOptions) (*cluster.TLSConfig, error) {
	if opts.tlsCertFile == "" && opts.tlsKeyFile == "" && opts.tlsClientCAFile == "" {
		if opts.tlsRequireClientCert {
			return nil, errors.New("--tls-require-client-cert needs --tls-cert-file and --tls-client-ca-file")
		}
		return nil, nil
	}
	minVersion, err := tlsMinVersion(opts.tlsMinVersion)
	if err != nil {
		return nil, err
	}
	return &cluster.TLSConfig{
		CertFile:          opts.tlsCertFile,
		KeyFile:           opts.tlsKeyFile,
		ClientCAFile:      opts.tlsClientCAFile,
		RequireClientCert: opts.tlsRequireClientCert,
		MinVersion:        minVersion,
	}, nil
}

func tlsMinVersion(name string) (uint16, error) {
	switch name {
	case "", "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	default:
		return 0, fmt.Errorf("--tls-min-version must be 1.2 or 1.3, got %q", name)
	}
}

func shutdownClusterNode(node *cluster.Node, runErr error) error {
	nodeErr := node.Shutdown()
	fsmErr := node.FSM().Close()

	return joinErrors(runErr, nodeErr, fsmErr)
}
