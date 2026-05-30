package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/teddymalhan/pallasdb/cluster"
	"github.com/teddymalhan/pallasdb/pallasdb"
	"github.com/teddymalhan/pallasdb/pallasdbgrpc"
	"google.golang.org/grpc"
)

func main() {
	addr := flag.String("addr", ":50051", "gRPC listen address")
	dataDir := flag.String("data-dir", "data", "KV database directory")
	raftAddr := flag.String("raft-addr", ":7001", "Raft TCP transport address")
	raftDir := flag.String("raft-dir", "raft", "Raft state directory (BoltDB + snapshots)")
	nodeID := flag.String("node-id", "", "unique node ID within the cluster (required)")
	joinAddr := flag.String("join", "", "HTTP management address of an existing node to join; empty to bootstrap")
	httpAddr := flag.String("http-addr", ":8001", "HTTP management address (join endpoint)")
	applyTimeout := flag.Duration("apply-timeout", 10*time.Second, "Raft apply/barrier timeout")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "graceful shutdown timeout")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	if *nodeID == "" {
		logger.Error("--node-id is required")
		os.Exit(1)
	}

	store, err := pallasdb.NewKV(*dataDir, pallasdb.WithAutoCompact(false))
	if err != nil {
		logger.Error("open database", "err", err)
		os.Exit(1)
	}

	nodeCfg := cluster.Config{
		NodeID:    *nodeID,
		RaftAddr:  *raftAddr,
		RaftDir:   *raftDir,
		Bootstrap: *joinAddr == "",
		JoinAddr:  *joinAddr,
		Timeout:   *applyTimeout,
	}

	node, err := cluster.Open(store, nodeCfg)
	if err != nil {
		logger.Error("open cluster node", "err", err)
		_ = store.Close()
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// HTTP management server (join endpoint).
	mux := http.NewServeMux()
	mux.Handle("/cluster/join", cluster.JoinHandler(node))
	httpSrv := &http.Server{Addr: *httpAddr, Handler: mux}
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server", "err", err)
		}
	}()

	// gRPC server.
	listenConfig := net.ListenConfig{}
	lis, err := listenConfig.Listen(ctx, "tcp", *addr)
	if err != nil {
		logger.Error("listen", "addr", *addr, "err", err)
		_ = node.Shutdown()
		os.Exit(1)
	}

	srv := pallasdbgrpc.NewClusterGRPCServer(
		node.FSM(),
		node.Raft(),
		*applyTimeout,
		grpc.ChainUnaryInterceptor(pallasdbgrpc.LoggingUnaryInterceptor(logger)),
	)

	logger.Info("starting cluster gRPC server",
		"addr", *addr,
		"raft_addr", *raftAddr,
		"http_addr", *httpAddr,
		"node_id", *nodeID,
		"data_dir", *dataDir,
	)

	if err := pallasdbgrpc.ServeWithGracefulStopTimeout(ctx, lis, srv, *shutdownTimeout); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("serve", "err", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), *shutdownTimeout)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)

	if err := node.Shutdown(); err != nil {
		logger.Error("shutdown raft node", "err", err)
	}
	if err := node.FSM().Close(); err != nil {
		logger.Error("close fsm store", "err", err)
	}

	logger.Info("cluster node stopped")
}
