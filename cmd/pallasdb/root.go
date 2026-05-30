package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const defaultShutdownTimeout = 15 * time.Second

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type rootOptions struct {
	logFormat       string
	shutdownTimeout time.Duration
}

func newRootCommand() *cobra.Command {
	opts := &rootOptions{}
	config := newConfigOptions()

	cmd := &cobra.Command{
		Use:           "pallasdb",
		Short:         "PallasDB is an embedded and distributed key-value database",
		Long:          "PallasDB is an embedded and distributed key-value database built on an LSM storage engine and Raft replication.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if err := config.load(cmd); err != nil {
				return err
			}
			config.apply()
			return nil
		},
	}

	cmd.PersistentFlags().StringVar(&config.configFile, "config", "", "config file path")
	cmd.PersistentFlags().StringVar(&opts.logFormat, "log-format", "text", "log format: text or json")
	cmd.PersistentFlags().DurationVar(&opts.shutdownTimeout, "shutdown-timeout", defaultShutdownTimeout, "graceful shutdown timeout")
	config.bindFlag(cmd.PersistentFlags(), "log-format", "log.format")
	config.bindFlag(cmd.PersistentFlags(), "shutdown-timeout", "shutdown.timeout")
	config.registerApply(func(v *viper.Viper) {
		opts.logFormat = v.GetString("log.format")
		opts.shutdownTimeout = v.GetDuration("shutdown.timeout")
	})

	cmd.AddCommand(
		newLocalCommand(opts, config),
		newServeCommand(opts, config),
		newClusterCommand(opts, config),
		newCompletionCommand(),
		newVersionCommand(),
	)

	return cmd
}

func newLogger(format string) (*slog.Logger, error) {
	switch strings.ToLower(format) {
	case "text":
		return slog.New(slog.NewTextHandler(os.Stderr, nil)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(os.Stderr, nil)), nil
	default:
		return nil, fmt.Errorf("unsupported log format %q", format)
	}
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "pallasdb %s\ncommit: %s\nbuilt: %s\n", version, commit, date)
			return err
		},
	}
}

func requirePositiveDuration(name string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	return nil
}

func requireNonEmptyFlag(name, value string) error {
	if value == "" {
		return fmt.Errorf("--%s is required", name)
	}
	return nil
}

func joinErrors(errs ...error) error {
	return errors.Join(errs...)
}
