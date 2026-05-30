package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/teddymalhan/gokv/gokv"
	"github.com/teddymalhan/gokv/gokvgrpc"
	"google.golang.org/grpc"
)

func main() {
	addr := flag.String("addr", ":50051", "gRPC listen address")
	dataDir := flag.String("data-dir", "data", "database directory")
	shutdownTimeout := flag.Duration("shutdown-timeout", 15*time.Second, "graceful shutdown timeout")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	store, err := gokv.NewKV(*dataDir)
	if err != nil {
		logger.Error("open database", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("close database", "err", err)
		}
	}()

	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		logger.Error("listen", "addr", *addr, "err", err)
		os.Exit(1)
	}

	srv := gokvgrpc.NewGRPCServer(
		store,
		grpc.ChainUnaryInterceptor(gokvgrpc.LoggingUnaryInterceptor(logger)),
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting gRPC server", "addr", *addr, "data_dir", *dataDir)
	if err := gokvgrpc.ServeWithGracefulStopTimeout(ctx, lis, srv, *shutdownTimeout); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("serve", "err", err)
		gokvgrpc.GracefulStop(srv, *shutdownTimeout)
		os.Exit(1)
	}
	logger.Info("gRPC server stopped")
}
