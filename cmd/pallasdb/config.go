package main

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const envPrefix = "PALLASDB"

// Config is the typed, validated projection of every resolved setting: config
// file, environment, and flags collapsed into one struct. Every key viper knows
// about must appear here — decoding runs with ErrorUnused, so an unknown or
// misspelled key is a hard failure rather than a silently ignored line.
type Config struct {
	Log      LogConfig      `mapstructure:"log"`
	Shutdown ShutdownConfig `mapstructure:"shutdown"`
	Local    LocalConfig    `mapstructure:"local"`
	Serve    ServeConfig    `mapstructure:"serve"`
	Cluster  ClusterConfig  `mapstructure:"cluster"`
	Cache    CacheConfig    `mapstructure:"cache"`
	Client   ClientConfig   `mapstructure:"client"`
}

// LogConfig controls process logging.
type LogConfig struct {
	Format string `mapstructure:"format"`
}

// ShutdownConfig controls graceful shutdown of long-running servers.
type ShutdownConfig struct {
	Timeout time.Duration `mapstructure:"timeout"`
}

// LocalConfig controls the in-process `local *` commands.
type LocalConfig struct {
	DataDir string `mapstructure:"data_dir"`
}

// ServeConfig controls `serve *`.
type ServeConfig struct {
	GRPC ServeGRPCConfig `mapstructure:"grpc"`
}

// ServeGRPCConfig controls the single-node gRPC server.
type ServeGRPCConfig struct {
	Addr    string `mapstructure:"addr"`
	DataDir string `mapstructure:"data_dir"`
}

// ClusterConfig controls `cluster *`.
type ClusterConfig struct {
	GRPCAddr     string        `mapstructure:"grpc_addr"`
	DataDir      string        `mapstructure:"data_dir"`
	RaftAddr     string        `mapstructure:"raft_addr"`
	RaftDir      string        `mapstructure:"raft_dir"`
	NodeID       string        `mapstructure:"node_id"`
	Join         string        `mapstructure:"join"`
	ApplyTimeout time.Duration `mapstructure:"apply_timeout"`

	// Membership policy. Defaults are declared by the `cluster start` flags in
	// cluster.go rather than by setConfigDefaults, so that the flag and the
	// config key cannot drift apart.
	BootstrapExpect    int           `mapstructure:"bootstrap_expect"`
	NonVoter           bool          `mapstructure:"non_voter"`
	LeaveOnShutdown    bool          `mapstructure:"leave_on_shutdown"`
	FailureGracePeriod time.Duration `mapstructure:"failure_grace_period"`

	Serf SerfConfig       `mapstructure:"serf"`
	TLS  ClusterTLSConfig `mapstructure:"tls"`
}

// SerfConfig controls gossip-based cluster discovery.
type SerfConfig struct {
	Enabled       bool     `mapstructure:"enabled"`
	Addr          string   `mapstructure:"addr"`
	AdvertiseAddr string   `mapstructure:"advertise_addr"`
	Join          []string `mapstructure:"join"`
	EventBuffer   int      `mapstructure:"event_buffer"`
	// EncryptKey is a base64 gossip encryption key decoding to 16, 24 or 32
	// bytes. Empty disables gossip encryption.
	EncryptKey string `mapstructure:"encrypt_key"`
}

// ClusterTLSConfig controls transport security between cluster nodes.
type ClusterTLSConfig struct {
	CertFile          string `mapstructure:"cert_file"`
	KeyFile           string `mapstructure:"key_file"`
	ClientCAFile      string `mapstructure:"client_ca_file"`
	RequireClientCert bool   `mapstructure:"require_client_cert"`
	MinVersion        string `mapstructure:"min_version"`
}

// CacheConfig controls the optional in-process read cache.
type CacheConfig struct {
	Enabled      bool  `mapstructure:"enabled"`
	MaxCostBytes int64 `mapstructure:"max_cost_bytes"`
	NumCounters  int64 `mapstructure:"num_counters"`
}

// ClientConfig controls the commands that talk to a running server.
type ClientConfig struct {
	Addr         string        `mapstructure:"addr"`
	Timeout      time.Duration `mapstructure:"timeout"`
	MaxRedirects int           `mapstructure:"max_redirects"`
}

// configOptions owns the viper instance and the per-command hooks that copy
// resolved settings into each command's option struct.
type configOptions struct {
	configFile string
	viper      *viper.Viper
	applyFuncs map[*cobra.Command][]func(*viper.Viper)
	pending    []func(*viper.Viper)
}

func newConfigOptions() *configOptions {
	v := viper.New()
	v.SetEnvPrefix(envPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()

	v.SetConfigName("pallasdb")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("$HOME/.pallasdb")
	v.AddConfigPath("/etc/pallasdb")

	setConfigDefaults(v)

	return &configOptions{
		viper:      v,
		applyFuncs: map[*cobra.Command][]func(*viper.Viper){},
	}
}

func setConfigDefaults(v *viper.Viper) {
	v.SetDefault("log.format", "text")
	v.SetDefault("shutdown.timeout", defaultShutdownTimeout)
	v.SetDefault("local.data_dir", "data")
	v.SetDefault("serve.grpc.addr", ":50051")
	v.SetDefault("serve.grpc.data_dir", "data")
	v.SetDefault("cluster.grpc_addr", ":50051")
	v.SetDefault("cluster.data_dir", "data")
	v.SetDefault("cluster.raft_addr", ":7001")
	v.SetDefault("cluster.raft_dir", "raft")
	v.SetDefault("cluster.node_id", "")
	v.SetDefault("cluster.join", "")
	v.SetDefault("cluster.apply_timeout", 10*time.Second)
	v.SetDefault("cluster.serf.enabled", true)
	v.SetDefault("cluster.serf.addr", ":7946")
	v.SetDefault("cluster.serf.advertise_addr", "")
	v.SetDefault("cluster.serf.join", []string{})
	v.SetDefault("cluster.serf.event_buffer", 64)
	v.SetDefault("cache.enabled", false)
	v.SetDefault("cache.max_cost_bytes", int64(32*1024*1024))
	v.SetDefault("cache.num_counters", int64(1_000_000))
	v.SetDefault("client.addr", defaultClientAddr)
	v.SetDefault("client.timeout", defaultClientTimeout)
	v.SetDefault("client.max_redirects", defaultClientMaxRedirects)
}

// load reads the config file, decodes every resolved setting into the typed
// Config, and validates it in the scope of the command about to run.
func (opts *configOptions) load(cmd *cobra.Command) error {
	if opts.configFile != "" {
		opts.viper.SetConfigFile(opts.configFile)
	}

	if err := opts.viper.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if opts.configFile != "" || !errors.As(err, &notFound) {
			return fmt.Errorf("read config: %w", err)
		}
	}

	cfg := &Config{}
	if err := opts.viper.Unmarshal(cfg, decoderOptions...); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if err := cfg.validate(commandPath(cmd)); err != nil {
		return err
	}
	return nil
}

// decoderOptions makes decoding strict about unknown keys while staying as
// permissive about scalar types as viper's own Get* accessors are, since
// environment variables always arrive as strings.
var decoderOptions = []viper.DecoderConfigOption{
	func(dc *mapstructure.DecoderConfig) {
		dc.ErrorUnused = true
		dc.WeaklyTypedInput = true
	},
}

// registerApply records a hook that copies resolved settings into the option
// struct of the command currently being constructed. The hook stays pending
// until scope attaches it to a command.
func (opts *configOptions) registerApply(apply func(*viper.Viper)) {
	opts.pending = append(opts.pending, apply)
}

// scope attaches every hook registered while cmd's subtree was constructed to
// cmd itself, and returns cmd so it reads naturally at the AddCommand call:
//
//	root.AddCommand(config.scope(newServeCommand(opts, config)))
//
// Hooks then run only for the command actually being executed and for its
// ancestors, instead of every hook running on every invocation.
func (opts *configOptions) scope(cmd *cobra.Command) *cobra.Command {
	opts.applyFuncs[cmd] = append(opts.applyFuncs[cmd], opts.pending...)
	opts.pending = opts.pending[:0]
	return cmd
}

// apply runs the hooks owned by cmd and its ancestors, outermost first.
func (opts *configOptions) apply(cmd *cobra.Command) {
	var chain []*cobra.Command
	for c := cmd; c != nil; c = c.Parent() {
		chain = append(chain, c)
	}
	for i := len(chain) - 1; i >= 0; i-- {
		for _, hook := range opts.applyFuncs[chain[i]] {
			hook(opts.viper)
		}
	}
}

func (opts *configOptions) bindFlag(flags *pflag.FlagSet, flagName, key string) {
	flag := flags.Lookup(flagName)
	if flag == nil {
		panic(fmt.Sprintf("bind config key %q: flag %q not found", key, flagName))
	}
	if err := opts.viper.BindPFlag(key, flag); err != nil {
		panic(fmt.Sprintf("bind config key %q to flag %q: %v", key, flagName, err))
	}
}

// commandPath is the space-separated path of the command being executed, e.g.
// "pallasdb cluster start".
func commandPath(cmd *cobra.Command) string {
	if cmd == nil {
		return ""
	}
	return cmd.CommandPath()
}

// validate rejects a resolved configuration that cannot possibly work. Checks
// that depend on which command is running are keyed off path; everything else
// is checked unconditionally, because a broken value in a config file should
// fail loudly no matter which subcommand happens to read it.
func (cfg *Config) validate(path string) error {
	var errs []error

	if _, err := logHandlerKind(cfg.Log.Format); err != nil {
		errs = append(errs, err)
	}
	if cfg.Shutdown.Timeout <= 0 {
		errs = append(errs, errors.New("shutdown.timeout must be positive"))
	}
	if cfg.Cluster.ApplyTimeout <= 0 {
		errs = append(errs, errors.New("cluster.apply_timeout must be positive"))
	}
	if cfg.Cluster.Serf.EventBuffer <= 0 {
		errs = append(errs, errors.New("cluster.serf.event_buffer must be positive"))
	}

	errs = append(errs,
		validateHostPort("serve.grpc.addr", cfg.Serve.GRPC.Addr, false),
		validateHostPort("cluster.grpc_addr", cfg.Cluster.GRPCAddr, false),
		validateHostPort("cluster.raft_addr", cfg.Cluster.RaftAddr, false),
		validateHostPort("cluster.join", cfg.Cluster.Join, true),
		validateHostPort("cluster.serf.addr", cfg.Cluster.Serf.Addr, false),
		validateHostPort("cluster.serf.advertise_addr", cfg.Cluster.Serf.AdvertiseAddr, true),
		validateHostPort("client.addr", cfg.Client.Addr, true),
	)
	for i, addr := range cfg.Cluster.Serf.Join {
		errs = append(errs, validateHostPort(fmt.Sprintf("cluster.serf.join[%d]", i), addr, false))
	}

	if err := cfg.Cache.validate(); err != nil {
		errs = append(errs, err)
	}
	if cfg.Client.Timeout < 0 {
		errs = append(errs, errors.New("client.timeout must not be negative"))
	}
	if cfg.Client.MaxRedirects < 0 {
		errs = append(errs, errors.New("client.max_redirects must not be negative"))
	}

	// A node that is about to join a Raft cluster must be identifiable.
	if path == "pallasdb cluster start" && strings.TrimSpace(cfg.Cluster.NodeID) == "" {
		errs = append(errs, errors.New("cluster.node_id must not be empty (set --node-id)"))
	}

	return joinErrors(errs...)
}

// validate keeps the ristretto sizing self-consistent: counters track admission
// for individual items, so budgeting more counters than the cache has bytes
// always indicates a transposed pair of values.
func (c *CacheConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if c.MaxCostBytes <= 0 {
		errs = append(errs, errors.New("cache.max_cost_bytes must be positive when cache.enabled is true"))
	}
	if c.NumCounters <= 0 {
		errs = append(errs, errors.New("cache.num_counters must be positive when cache.enabled is true"))
	}
	if c.MaxCostBytes > 0 && c.NumCounters > c.MaxCostBytes {
		errs = append(errs, fmt.Errorf(
			"cache.num_counters (%d) must not exceed cache.max_cost_bytes (%d); counters should be ~10x the expected item count",
			c.NumCounters, c.MaxCostBytes,
		))
	}
	return joinErrors(errs...)
}

// validateHostPort checks a "host:port" listen or dial address. An empty host
// means "all interfaces"; an empty address is only allowed when optional.
func validateHostPort(field, addr string, optional bool) error {
	if addr == "" {
		if optional {
			return nil
		}
		return fmt.Errorf("%s must not be empty", field)
	}

	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s: %q is not a host:port address", field, addr)
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 0 || portNum > 65535 {
		return fmt.Errorf("%s: %q has an invalid port %q", field, addr, port)
	}
	if strings.ContainsAny(host, " \t/") {
		return fmt.Errorf("%s: %q has an invalid host %q", field, addr, host)
	}
	return nil
}
