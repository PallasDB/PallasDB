package main

import (
	"context"
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

	serfEnabled       bool
	serfAddr          string
	serfAdvertiseAddr string
	serfJoinAddrs     []string
	serfEventBuffer   int
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

			kvOpts := append([]db.KVOption{db.WithAutoCompact(false)}, kvOptsFromViper(config.viper)...)
			store, err := db.NewKV(opts.dataDir, kvOpts...)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}

			nodeCfg := cluster.Config{
				NodeID:    opts.nodeID,
				GRPCAddr:  opts.grpcAddr,
				RaftAddr:  opts.raftAddr,
				RaftDir:   opts.raftDir,
				Bootstrap: shouldBootstrapCluster(opts),
				JoinAddr:  opts.joinAddr,
				Timeout:   opts.applyTimeout,

				SerfEnabled:       opts.serfEnabled,
				SerfAddr:          opts.serfAddr,
				SerfAdvertiseAddr: opts.serfAdvertiseAddr,
				SerfJoinAddrs:     opts.serfJoinAddrs,
				SerfEventBuffer:   opts.serfEventBuffer,
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
	cmd.Flags().BoolVar(&opts.serfEnabled, "serf-enabled", true, "enable Serf gossip discovery")
	cmd.Flags().StringVar(&opts.serfAddr, "serf-addr", ":7946", "Serf gossip bind address")
	cmd.Flags().StringVar(&opts.serfAdvertiseAddr, "serf-advertise-addr", "", "Serf advertise address")
	cmd.Flags().StringSliceVar(&opts.serfJoinAddrs, "serf-join", nil, "Serf addresses of existing nodes")
	cmd.Flags().IntVar(&opts.serfEventBuffer, "serf-event-buffer", 64, "Serf event channel buffer size")
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
	config.bindFlag(cmd.Flags(), "serf-enabled", "cluster.serf.enabled")
	config.bindFlag(cmd.Flags(), "serf-addr", "cluster.serf.addr")
	config.bindFlag(cmd.Flags(), "serf-advertise-addr", "cluster.serf.advertise_addr")
	config.bindFlag(cmd.Flags(), "serf-join", "cluster.serf.join")
	config.bindFlag(cmd.Flags(), "serf-event-buffer", "cluster.serf.event_buffer")
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
		opts.serfEnabled = v.GetBool("cluster.serf.enabled")
		opts.serfAddr = v.GetString("cluster.serf.addr")
		opts.serfAdvertiseAddr = v.GetString("cluster.serf.advertise_addr")
		opts.serfJoinAddrs = v.GetStringSlice("cluster.serf.join")
		opts.serfEventBuffer = v.GetInt("cluster.serf.event_buffer")
	})
	return cmd
}

func shouldBootstrapCluster(opts *clusterStartOptions) bool {
	if !opts.serfEnabled {
		return opts.joinAddr == ""
	}
	return opts.joinAddr == "" && len(opts.serfJoinAddrs) == 0
}

func validateClusterStartOptions(root *rootOptions, opts *clusterStartOptions) error {
	return joinErrors(
		requireNonEmptyFlag("node-id", opts.nodeID),
		requirePositiveDuration("--apply-timeout", opts.applyTimeout),
		requirePositiveDuration("--shutdown-timeout", root.shutdownTimeout),
		requirePositiveInt("--serf-event-buffer", opts.serfEventBuffer),
	)
}

func shutdownClusterNode(node *cluster.Node, runErr error) error {
	nodeErr := node.Shutdown()
	fsmErr := node.FSM().Close()

	return joinErrors(runErr, nodeErr, fsmErr)
}
