package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/teddymalhan/pallasdb/client"
)

const (
	defaultClientAddr         = "127.0.0.1:50051"
	defaultClientTimeout      = 30 * time.Second
	defaultClientMaxRedirects = 1
)

// remoteOptions holds the connection settings shared by every command that
// talks to a running PallasDB server.
type remoteOptions struct {
	addr         string
	timeout      time.Duration
	maxRedirects int
	consistency  string
}

// addRemoteFlags declares the connection flags on cmd and registers the hook
// that fills them in from the resolved config.
//
// The flags are declared per command rather than bound to viper because viper
// binds a single flag per config key, and `kv`, `sql`, `dump` and `restore` are
// siblings that each need their own --addr. Precedence (flag > env > file >
// default) is therefore resolved explicitly here.
func addRemoteFlags(cmd *cobra.Command, config *configOptions) *remoteOptions {
	opts := &remoteOptions{}

	flags := cmd.PersistentFlags()
	flags.StringVar(&opts.addr, "addr", defaultClientAddr, "gRPC address of a PallasDB server")
	flags.DurationVar(&opts.timeout, "timeout", defaultClientTimeout, "per-command timeout; 0 waits forever")
	flags.IntVar(&opts.maxRedirects, "max-redirects", defaultClientMaxRedirects, "leader redirects to follow per call")
	flags.StringVar(&opts.consistency, "consistency", "default", "read consistency: default, linearizable, or stale")

	config.registerApply(func(v *viper.Viper) {
		if !flagChanged(cmd, "addr") {
			opts.addr = v.GetString("client.addr")
		}
		if !flagChanged(cmd, "timeout") {
			opts.timeout = v.GetDuration("client.timeout")
		}
		if !flagChanged(cmd, "max-redirects") {
			opts.maxRedirects = v.GetInt("client.max_redirects")
		}
	})
	return opts
}

// connect dials the configured server. The caller closes the client.
func (opts *remoteOptions) connect() (*client.Client, error) {
	if opts.addr == "" {
		return nil, fmt.Errorf("--addr is required")
	}
	return client.New(opts.addr, client.WithMaxRedirects(opts.maxRedirects))
}

// consistencyLevel parses the --consistency flag.
func (opts *remoteOptions) consistencyLevel() (client.Consistency, error) {
	return client.ParseConsistency(opts.consistency)
}

// sessionContext derives the context a remote command runs under: the caller's
// context, additionally cancelled by SIGINT or SIGTERM.
func (opts *remoteOptions) sessionContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// callContext bounds a single server call by --timeout. A non-positive timeout
// waits for as long as the session lives.
func (opts *remoteOptions) callContext(parent context.Context) (context.Context, context.CancelFunc) {
	if opts.timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, opts.timeout)
}

// withSessionClient runs fn against a freshly dialled client and a context that
// is cancelled by Ctrl-C but is deliberately not bounded by --timeout. Use it
// for open-ended work — an interactive session, a whole-keyspace dump — and
// bound the individual calls inside fn with callContext instead.
func (opts *remoteOptions) withSessionClient(cmd *cobra.Command, fn func(context.Context, *client.Client) error) error {
	c, err := opts.connect()
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()

	ctx, stop := opts.sessionContext(cmd)
	defer stop()

	return fn(ctx, c)
}

// withClient runs fn against a freshly dialled client and a context bounded by
// --timeout, which is what a single-shot command wants.
func (opts *remoteOptions) withClient(cmd *cobra.Command, fn func(context.Context, *client.Client) error) error {
	return opts.withSessionClient(cmd, func(sessionCtx context.Context, c *client.Client) error {
		ctx, cancel := opts.callContext(sessionCtx)
		defer cancel()
		return fn(ctx, c)
	})
}
