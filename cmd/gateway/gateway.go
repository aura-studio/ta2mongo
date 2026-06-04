// Package gateway implements the gateway runtime role (role.mode = gateway): a
// long-running HTTP gateway exposing the /upload log-reporting API.
package gateway

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/role/gateway"
)

// Run executes the gateway role against an already-loaded config: it starts the
// HTTP server and serves until interrupted. The runtime role is selected by
// config key role.mode, dispatched from main.
func Run(cmd *cobra.Command, c *config.Config) error {
	srv, err := gateway.New(cmd.Context(), c.Dao, c.Process, c.Parser.Filter, *c.Role.Gateway)
	if err != nil {
		return err
	}
	defer srv.Close()
	if err := srv.EnsureIndexes(cmd.Context()); err != nil {
		return err
	}
	// Listen address comes from the config key role.gateway.addr,
	// settable via file / TANGO_ROLE_GATEWAY_ADDR / --role.gateway.addr.
	return srv.Run(cmd.Context(), c.Role.Gateway.Addr)
}
