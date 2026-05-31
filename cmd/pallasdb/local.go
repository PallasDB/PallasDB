package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"github.com/teddymalhan/pallasdb/db"
)

type localOptions struct {
	dataDir string
}

func newLocalCommand(root *rootOptions, config *configOptions) *cobra.Command {
	opts := &localOptions{}
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Operate directly on a local PallasDB data directory",
		Args:  cobra.NoArgs,
	}
	cmd.PersistentFlags().StringVar(&opts.dataDir, "data-dir", "data", "database directory")
	config.bindFlag(cmd.PersistentFlags(), "data-dir", "local.data_dir")
	config.registerApply(func(v *viper.Viper) {
		opts.dataDir = v.GetString("local.data_dir")
	})

	cmd.AddCommand(
		newLocalGetCommand(root, opts),
		newLocalPutCommand(root, opts),
		newLocalDeleteCommand(root, opts),
		newLocalRangeCommand(root, opts),
		newLocalCompactCommand(root, opts),
		newLocalBenchmarkCommand(root, opts),
	)
	return cmd
}

func openLocalStore(opts *localOptions) (*db.KV, error) {
	return db.NewKV(opts.dataDir)
}

func newLocalGetCommand(_ *rootOptions, opts *localOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "get KEY",
		Short: "Read a key from a local database",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openLocalStore(opts)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = store.Close() }()

			value, ok, err := store.Get([]byte(args[0]))
			if err != nil {
				return fmt.Errorf("get key: %w", err)
			}
			if !ok {
				return fmt.Errorf("key not found: %s", args[0])
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(value))
			return err
		},
	}
}

func newLocalPutCommand(_ *rootOptions, opts *localOptions) *cobra.Command {
	var mode string
	cmd := &cobra.Command{
		Use:   "put KEY VALUE",
		Short: "Write a key to a local database",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			updateMode, err := parseUpdateMode(mode)
			if err != nil {
				return err
			}

			store, err := openLocalStore(opts)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = store.Close() }()

			updated, err := store.SetEx([]byte(args[0]), []byte(args[1]), updateMode)
			if err != nil {
				return fmt.Errorf("put key: %w", err)
			}
			if updated {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "updated")
			} else {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), "unchanged")
			}
			return err
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "upsert", "write mode: upsert, insert, or update")
	return cmd
}

func newLocalDeleteCommand(_ *rootOptions, opts *localOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "delete KEY",
		Aliases: []string{"del", "rm"},
		Short:   "Delete a key from a local database",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openLocalStore(opts)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = store.Close() }()

			deleted, err := store.Del([]byte(args[0]))
			if err != nil {
				return fmt.Errorf("delete key: %w", err)
			}
			if !deleted {
				return fmt.Errorf("key not found: %s", args[0])
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "deleted")
			return err
		},
	}
}

func newLocalRangeCommand(_ *rootOptions, opts *localOptions) *cobra.Command {
	var descending bool
	cmd := &cobra.Command{
		Use:   "range START STOP",
		Short: "Scan a local key range",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := openLocalStore(opts)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = store.Close() }()

			tx := store.NewTX()
			defer tx.Abort()

			iter, err := tx.Range([]byte(args[0]), []byte(args[1]), descending)
			if err != nil {
				return fmt.Errorf("range keys: %w", err)
			}
			for ; iter.Valid(); err = iter.Next() {
				if err != nil {
					return fmt.Errorf("iterate range: %w", err)
				}
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", iter.Key(), iter.Val()); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&descending, "descending", false, "scan in descending order")
	return cmd
}

func newLocalCompactCommand(_ *rootOptions, opts *localOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "compact",
		Short: "Run local database compaction once",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			store, err := openLocalStore(opts)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer func() { _ = store.Close() }()

			if err := store.Compact(); err != nil {
				return fmt.Errorf("compact database: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "compacted")
			return err
		},
	}
}

func parseUpdateMode(mode string) (db.UpdateMode, error) {
	switch mode {
	case "upsert":
		return db.ModeUpsert, nil
	case "insert":
		return db.ModeInsert, nil
	case "update":
		return db.ModeUpdate, nil
	default:
		return db.ModeUnknown, fmt.Errorf("invalid write mode %q", mode)
	}
}
