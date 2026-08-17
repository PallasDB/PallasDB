package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/teddymalhan/pallasdb/db"
	grpcapi "github.com/teddymalhan/pallasdb/grpc"
)

func kvOptsFromViper(v *viper.Viper) []db.KVOption {
	if !v.GetBool("cache.enabled") {
		return nil
	}
	return []db.KVOption{db.WithCache(
		v.GetInt64("cache.max_cost_bytes"),
		v.GetInt64("cache.num_counters"),
	)}
}

type serveGRPCOptions struct {
	addr    string
	dataDir string
}

func newServeCommand(root *rootOptions, config *configOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run PallasDB servers",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newServeGRPCCommand(root, config))
	return cmd
}

func newServeGRPCCommand(root *rootOptions, config *configOptions) *cobra.Command {
	opts := &serveGRPCOptions{}
	cmd := &cobra.Command{
		Use:   "grpc",
		Short: "Run a single-node gRPC server",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := requirePositiveDuration("--shutdown-timeout", root.shutdownTimeout); err != nil {
				return err
			}
			logger, err := newLogger(root.logFormat)
			if err != nil {
				return err
			}

			// One owner for the engine: db.DB embeds the KV by value, so the
			// server takes &database.KV rather than a second engine over the
			// same directory, and Close happens exactly once.
			database, err := db.NewDB(opts.dataDir, kvOptsFromViper(config.viper)...)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() {
				if err := database.Close(); err != nil {
					logger.Error("close database", "err", err)
				}
			}()

			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			listenConfig := net.ListenConfig{}
			lis, err := listenConfig.Listen(ctx, "tcp", opts.addr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w", opts.addr, err)
			}

			srv := grpcapi.NewGRPCServer(
				&database.KV,
				grpcapi.WithLogger(logger),
				grpcapi.WithSQL(grpcapi.NewDBExecutor(database)),
				grpcapi.WithReflection(),
			)

			logger.Info("starting gRPC server", "addr", opts.addr, "data_dir", opts.dataDir)
			if err := grpcapi.ServeWithGracefulStopTimeout(ctx, lis, srv, root.shutdownTimeout); err != nil && !errors.Is(err, context.Canceled) {
				grpcapi.GracefulStop(srv, root.shutdownTimeout)
				return fmt.Errorf("serve grpc: %w", err)
			}
			logger.Info("gRPC server stopped")
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.addr, "addr", ":50051", "gRPC listen address")
	cmd.Flags().StringVar(&opts.dataDir, "data-dir", "data", "database directory")
	cmd.Flags().Bool("cache-enabled", false, "enable in-process read cache")
	cmd.Flags().Int64("cache-max-cost", 32*1024*1024, "max cache bytes")
	cmd.Flags().Int64("cache-num-counters", 1_000_000, "cache num counters (≈10× expected item count)")
	config.bindFlag(cmd.Flags(), "addr", "serve.grpc.addr")
	config.bindFlag(cmd.Flags(), "data-dir", "serve.grpc.data_dir")
	config.bindFlag(cmd.Flags(), "cache-enabled", "cache.enabled")
	config.bindFlag(cmd.Flags(), "cache-max-cost", "cache.max_cost_bytes")
	config.bindFlag(cmd.Flags(), "cache-num-counters", "cache.num_counters")
	config.registerApply(func(v *viper.Viper) {
		opts.addr = v.GetString("serve.grpc.addr")
		opts.dataDir = v.GetString("serve.grpc.data_dir")
	})
	return cmd
}
