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
}

func newClusterCommand(root *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Run and manage a Raft-backed PallasDB cluster",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newClusterStartCommand(root))
	return cmd
}

func newClusterStartCommand(root *rootOptions) *cobra.Command {
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

			store, err := db.NewKV(opts.dataDir, db.WithAutoCompact(false))
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}

			nodeCfg := cluster.Config{
				NodeID:    opts.nodeID,
				RaftAddr:  opts.raftAddr,
				RaftDir:   opts.raftDir,
				Bootstrap: opts.joinAddr == "",
				JoinAddr:  opts.joinAddr,
				Timeout:   opts.applyTimeout,
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
				node.FSM(),
				node.Raft(),
				opts.applyTimeout,
				grpc.ChainUnaryInterceptor(grpcapi.LoggingUnaryInterceptor(logger)),
			)

			logger.Info("starting cluster node",
				"grpc_addr", opts.grpcAddr,
				"raft_addr", opts.raftAddr,
				"node_id", opts.nodeID,
				"data_dir", opts.dataDir,
				"raft_dir", opts.raftDir,
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
	mustMarkFlagRequired(cmd, "node-id")
	return cmd
}

func validateClusterStartOptions(root *rootOptions, opts *clusterStartOptions) error {
	return joinErrors(
		requireNonEmptyFlag("node-id", opts.nodeID),
		requirePositiveDuration("--apply-timeout", opts.applyTimeout),
		requirePositiveDuration("--shutdown-timeout", root.shutdownTimeout),
	)
}

func shutdownClusterNode(node *cluster.Node, runErr error) error {
	nodeErr := node.Shutdown()
	fsmErr := node.FSM().Close()

	return joinErrors(runErr, nodeErr, fsmErr)
}

func mustMarkFlagRequired(cmd *cobra.Command, name string) {
	if err := cmd.MarkFlagRequired(name); err != nil {
		panic(err)
	}
}
