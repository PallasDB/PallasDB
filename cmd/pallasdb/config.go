package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

const envPrefix = "PALLASDB"

type configOptions struct {
	configFile string
	viper      *viper.Viper
	applyFuncs []func(*viper.Viper)
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

	return &configOptions{viper: v}
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
}

func (opts *configOptions) load(_ *cobra.Command) error {
	if opts.configFile != "" {
		opts.viper.SetConfigFile(opts.configFile)
	}

	if err := opts.viper.ReadInConfig(); err != nil {
		if opts.configFile == "" {
			var notFound viper.ConfigFileNotFoundError
			if errors.As(err, &notFound) {
				return nil
			}
		}
		return fmt.Errorf("read config: %w", err)
	}
	return nil
}

func (opts *configOptions) apply() {
	for _, apply := range opts.applyFuncs {
		apply(opts.viper)
	}
}

func (opts *configOptions) registerApply(apply func(*viper.Viper)) {
	opts.applyFuncs = append(opts.applyFuncs, apply)
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
