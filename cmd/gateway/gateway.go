// Package gateway implements the `tango gateway` command: a long-running HTTP
// gateway exposing the log-reporting functions (ingest / upload) on top of a
// connected client SDK.
package gateway

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/cmd/shared"
	"rocket-nano/tools/tango/internal/service/gateway"
)

// NewCommand builds the `tango gateway` command. It loads the unified gateway
// config (gateway.{yaml,yml,json}) and runs the HTTP server until interrupted.
func NewCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "HTTP gateway exposing the ingest and upload log-reporting APIs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cc, cli, logger, err := shared.ConnectClient(cmd, shared.GatewayConfig)
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
	cmd.Flags().String("runtime.mongo.uri", "", "MongoDB connection URI (config key runtime.mongo.uri)")
	cmd.Flags().String("runtime.logging.level", "", "log level: debug, info, warn, error (config key runtime.logging.level)")
	cmd.Flags().String("gateway.addr", "", "HTTP listen address (config key gateway.addr)")
	cmd.Flags().StringVar(&addr, "addr", "", "HTTP listen address; overrides the config addr")
	return cmd
}
