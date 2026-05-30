package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	httpAddr     string
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

			httpSrv := newClusterHTTPServer(opts.httpAddr, node)
			httpErrCh := make(chan error, 1)
			go func() {
				if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					httpErrCh <- err
					return
				}
				httpErrCh <- nil
			}()

			listenConfig := net.ListenConfig{}
			lis, err := listenConfig.Listen(ctx, "tcp", opts.grpcAddr)
			if err != nil {
				return shutdownClusterNode(node, httpSrv, root.shutdownTimeout, fmt.Errorf("listen on %s: %w", opts.grpcAddr, err))
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
				"http_addr", opts.httpAddr,
				"node_id", opts.nodeID,
				"data_dir", opts.dataDir,
				"raft_dir", opts.raftDir,
			)

			serveErrCh := make(chan error, 1)
			go func() {
				if err := srv.Serve(lis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
					serveErrCh <- err
					return
				}
				serveErrCh <- nil
			}()

			var runErr error
			serveDone := false
			select {
			case err := <-serveErrCh:
				serveDone = true
				if err != nil {
					runErr = fmt.Errorf("serve grpc: %w", err)
				}
			case err := <-httpErrCh:
				if err != nil {
					runErr = fmt.Errorf("serve http: %w", err)
				}
			case <-ctx.Done():
			}

			if !serveDone {
				grpcapi.GracefulStop(srv, root.shutdownTimeout)
				if err := <-serveErrCh; err != nil && runErr == nil {
					runErr = fmt.Errorf("serve grpc: %w", err)
				}
			}

			if err := shutdownClusterNode(node, httpSrv, root.shutdownTimeout, runErr); err != nil {
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
	cmd.Flags().StringVar(&opts.joinAddr, "join", "", "HTTP management address of an existing node to join; empty to bootstrap")
	cmd.Flags().StringVar(&opts.httpAddr, "http-addr", ":8001", "HTTP management listen address")
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

func newClusterHTTPServer(addr string, node *cluster.Node) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/cluster/join", cluster.JoinHandler(node))
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
}

func shutdownClusterNode(node *cluster.Node, httpSrv *http.Server, timeout time.Duration, runErr error) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	httpErr := httpSrv.Shutdown(shutdownCtx)
	nodeErr := node.Shutdown()
	fsmErr := node.FSM().Close()

	return joinErrors(runErr, httpErr, nodeErr, fsmErr)
}

func mustMarkFlagRequired(cmd *cobra.Command, name string) {
	if err := cmd.MarkFlagRequired(name); err != nil {
		panic(err)
	}
}
