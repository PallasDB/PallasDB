package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/teddymalhan/pallasdb/client"
)

// newKVCommand builds the `kv` command group: the same operations as
// `local *`, but issued against a running server over gRPC.
func newKVCommand(config *configOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kv",
		Short: "Operate on a running PallasDB server over gRPC",
		Long: "Operate on a running PallasDB server over gRPC.\n\n" +
			"These commands mirror `pallasdb local *` but talk to a server at --addr " +
			"instead of opening the data directory in-process. Against a cluster they " +
			"follow the Raft leader automatically.",
		Args: cobra.NoArgs,
	}
	remote := addRemoteFlags(cmd, config)

	cmd.AddCommand(
		newKVGetCommand(remote),
		newKVPutCommand(remote),
		newKVDeleteCommand(remote),
		newKVRangeCommand(remote),
	)
	return cmd
}

func newKVGetCommand(remote *remoteOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "get KEY",
		Short: "Read a key from a running server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			consistency, err := remote.consistencyLevel()
			if err != nil {
				return err
			}
			return remote.withClient(cmd, func(ctx context.Context, c *client.Client) error {
				value, err := c.Get(ctx, []byte(args[0]), consistency)
				if err != nil {
					if errors.Is(err, client.ErrNotFound) {
						return fmt.Errorf("key not found: %s", args[0])
					}
					return fmt.Errorf("get key: %w", err)
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(value))
				return err
			})
		},
	}
}

func newKVPutCommand(remote *remoteOptions) *cobra.Command {
	var mode string
	cmd := &cobra.Command{
		Use:   "put KEY VALUE",
		Short: "Write a key on a running server",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			putMode, err := client.ParsePutMode(mode)
			if err != nil {
				return err
			}
			return remote.withClient(cmd, func(ctx context.Context, c *client.Client) error {
				updated, err := c.Put(ctx, []byte(args[0]), []byte(args[1]), putMode)
				if err != nil {
					return fmt.Errorf("put key: %w", err)
				}
				if updated {
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "updated")
				} else {
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "unchanged")
				}
				return err
			})
		},
	}
	cmd.Flags().StringVar(&mode, "mode", "upsert", "write mode: upsert, insert, or update")
	return cmd
}

func newKVDeleteCommand(remote *remoteOptions) *cobra.Command {
	return &cobra.Command{
		Use:     "delete KEY",
		Aliases: []string{"del", "rm"},
		Short:   "Delete a key on a running server",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return remote.withClient(cmd, func(ctx context.Context, c *client.Client) error {
				if _, err := c.Delete(ctx, []byte(args[0])); err != nil {
					if errors.Is(err, client.ErrNotFound) {
						return fmt.Errorf("key not found: %s", args[0])
					}
					return fmt.Errorf("delete key: %w", err)
				}
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "deleted")
				return err
			})
		},
	}
}

func newKVRangeCommand(remote *remoteOptions) *cobra.Command {
	var (
		descending bool
		limit      uint64
		keysOnly   bool
	)
	cmd := &cobra.Command{
		Use:   "range [START [STOP]]",
		Short: "Scan a key range on a running server",
		Long: "Scan a key range on a running server.\n\n" +
			"An omitted or empty START scans from the first key and an omitted or " +
			"empty STOP scans through the last. Both bounds are inclusive, and " +
			"--descending needs an explicit START because it is the upper bound " +
			"the scan walks down from.\n\n" +
			"The server caps how many rows one scan returns and truncates without " +
			"reporting it, so a scan that stops at a round number is probably not " +
			"finished: re-run with START set to the last key printed. Use " +
			"`pallasdb dump` to export the whole keyspace, which resumes for you.",
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			consistency, err := remote.consistencyLevel()
			if err != nil {
				return err
			}
			req := client.RangeRequest{
				Descending:  descending,
				Limit:       limit,
				KeysOnly:    keysOnly,
				Consistency: consistency,
			}
			if len(args) > 0 {
				req.Start = []byte(args[0])
			}
			if len(args) > 1 {
				req.Stop = []byte(args[1])
			}

			return remote.withClient(cmd, func(ctx context.Context, c *client.Client) error {
				out := cmd.OutOrStdout()
				err := c.Range(ctx, req, func(kv client.KeyValue) error {
					var writeErr error
					if keysOnly {
						_, writeErr = fmt.Fprintf(out, "%s\n", kv.Key)
					} else {
						_, writeErr = fmt.Fprintf(out, "%s\t%s\n", kv.Key, kv.Value)
					}
					return writeErr
				})
				if err != nil {
					return fmt.Errorf("range keys: %w", err)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&descending, "descending", false, "scan in descending order")
	cmd.Flags().Uint64Var(&limit, "limit", 0, "maximum rows to return; 0 means the server cap")
	cmd.Flags().BoolVar(&keysOnly, "keys-only", false, "print keys without values")
	return cmd
}
