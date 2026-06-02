package client

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/internal/service/gateway"
)

func newServeCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP/REST server exposing the five client functions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cc, cli, logger, err := loadClient(cmd)
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
	cmd.Flags().StringVar(&addr, "addr", "", "HTTP listen address; overrides server.addr")
	return cmd
}
