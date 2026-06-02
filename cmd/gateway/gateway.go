// Package gateway implements the `tango gateway` command: a long-running HTTP
// gateway service exposing ingest / upload / backfill / sql / publish APIs on
// top of a connected client SDK.
package gateway

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/cmd/shared"
	"rocket-nano/tools/tango/internal/service/gateway"
)

// NewCommand builds the `tango gateway` parent command. It loads the unified
// gateway config (gateway.{yaml,yml,json}).
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "HTTP gateway service exposing ingest, upload, backfill, sql, and publish APIs",
	}
	cmd.PersistentFlags().String("runtime.mongo.uri", "", "MongoDB connection URI (config key runtime.mongo.uri)")
	cmd.PersistentFlags().String("runtime.logging.level", "", "log level: debug, info, warn, error (config key runtime.logging.level)")
	cmd.PersistentFlags().String("gateway.addr", "", "HTTP listen address (config key gateway.addr)")
	cmd.AddCommand(ServeCommand(shared.GatewayConfig))
	return cmd
}

// ServeCommand builds the `serve` subcommand bound to the given config loader.
// The legacy `tango client serve` wrapper reuses it with the legacy loader.
func ServeCommand(load shared.ClientLoader) *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP/REST server exposing the five client functions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cc, cli, logger, err := shared.ConnectClient(cmd, load)
			if err != nil {
				return err
			}
			defer cli.Close()
			if err := cli.EnsureIndexes(cmd.Context()); err != nil {
				return err
			}
			if addr == "" {
				addr = cc.Server.Addr
			}
			return gateway.New(cc, cli, logger).Run(cmd.Context(), addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "HTTP listen address; overrides the config addr")
	return cmd
}
