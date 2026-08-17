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
			config.apply(cmd)
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
	config.scope(cmd)

	cmd.AddCommand(
		config.scope(newLocalCommand(opts, config)),
		config.scope(newServeCommand(opts, config)),
		config.scope(newClusterCommand(opts, config)),
		config.scope(newKVCommand(config)),
		config.scope(newSQLCommand(config)),
		config.scope(newDumpCommand(config)),
		config.scope(newRestoreCommand(config)),
		newCompletionCommand(),
		newVersionCommand(),
	)

	return cmd
}

// handlerKind is the validated form of the --log-format flag.
type handlerKind int

const (
	handlerText handlerKind = iota
	handlerJSON
)

// logHandlerKind validates a log format. It is called from config validation so
// that a bad --log-format fails on every command, not just the ones that
// happen to build a logger.
func logHandlerKind(format string) (handlerKind, error) {
	switch strings.ToLower(format) {
	case "text":
		return handlerText, nil
	case "json":
		return handlerJSON, nil
	default:
		return handlerText, fmt.Errorf("log.format: unsupported log format %q (want text or json)", format)
	}
}

func newLogger(format string) (*slog.Logger, error) {
	kind, err := logHandlerKind(format)
	if err != nil {
		return nil, err
	}
	if kind == handlerJSON {
		return slog.New(slog.NewJSONHandler(os.Stderr, nil)), nil
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil)), nil
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

func requirePositiveInt(name string, n int) error {
	if n <= 0 {
		return fmt.Errorf("%s must be positive", name)
	}
	return nil
}

func joinErrors(errs ...error) error {
	return errors.Join(errs...)
}

// flagChanged reports whether the user set a flag explicitly on the command
// line. The remote commands (kv, sql, dump, restore) each carry their own
// --addr/--timeout flags, and viper can bind only one flag per config key, so
// their precedence over the config file is resolved here instead.
func flagChanged(cmd *cobra.Command, name string) bool {
	flag := cmd.Flags().Lookup(name)
	return flag != nil && flag.Changed
}
