// Package gateway implements the `tango gateway` command: a long-running HTTP
// gateway exposing the /upload log-reporting API.
package gateway

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/role/gateway"
)

// NewCommand builds the `tango gateway` command. It loads the unified config
// (gateway.{yaml,yml,json}) and runs the HTTP server until interrupted.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "HTTP gateway exposing the /upload log-reporting API",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := resolveConfigPath(configFlag(cmd), "gateway.yaml", "gateway.yml", "gateway.json")
			c, err := config.Load(path, cmd.Flags())
			if err != nil {
				return err
			}
			logging.Init(c.Logging.Level)

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
		},
	}
	// Every config key is also a --<key> flag (file / TANGO_* env / flag are
	// interchangeable). The role is the subcommand; --config is the root flag.
	config.RegisterFlags(cmd.Flags())
	return cmd
}

// configFlag reads the inherited --config persistent flag (empty when unset).
func configFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("config")
	return v
}

// resolveConfigPath returns the config file path to use. When flagVal is set
// (the --config flag) it is returned verbatim. Otherwise the first of the
// candidate filenames that exists in the binary's own directory is returned.
func resolveConfigPath(flagVal string, candidates ...string) string {
	if flagVal != "" {
		return flagVal
	}
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}
