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
	var addr string
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
			if addr == "" {
				addr = c.Role.Gateway.Addr
			}
			return srv.Run(cmd.Context(), addr)
		},
	}
	cmd.Flags().String("dao.mongo.uri", "", "MongoDB connection URI (config key dao.mongo.uri)")
	cmd.Flags().String("logging.level", "", "log level: debug, info, warn, error (config key logging.level)")
	cmd.Flags().String("role.gateway.addr", "", "HTTP listen address (config key role.gateway.addr)")
	cmd.Flags().StringVar(&addr, "addr", "", "HTTP listen address; overrides the config addr")
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
