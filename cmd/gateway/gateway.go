// Package gateway implements the `tango gateway` command: a long-running HTTP
// gateway exposing the log-reporting functions (ingest / upload) on top of a
// connected client SDK.
package gateway

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/role/gateway"
	sdk "rocket-nano/tools/tango/internal/role/gateway/client"
)

// NewCommand builds the `tango gateway` command. It loads the unified gateway
// config (gateway.{yaml,yml,json}) and runs the HTTP server until interrupted.
func NewCommand() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "HTTP gateway exposing the ingest and upload log-reporting APIs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cc, cli, err := connectClient(cmd, gatewayConfig)
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
			return gateway.New(cc, cli).Run(cmd.Context(), addr)
		},
	}
	cmd.Flags().String("runtime.mongo.uri", "", "MongoDB connection URI (config key runtime.mongo.uri)")
	cmd.Flags().String("runtime.logging.level", "", "log level: debug, info, warn, error (config key runtime.logging.level)")
	cmd.Flags().String("gateway.addr", "", "HTTP listen address (config key gateway.addr)")
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

// clientLoader resolves and loads the GatewayRuntimeConfig a gateway-driven
// command runs on.
type clientLoader func(cmd *cobra.Command) (gateway.GatewayRuntimeConfig, error)

// gatewayConfig loads gateway.{yaml,yml,json} via the unified RoleConfig schema
// and initializes the shared logger from its logging level.
func gatewayConfig(cmd *cobra.Command) (gateway.GatewayRuntimeConfig, error) {
	path := resolveConfigPath(configFlag(cmd), "gateway.yaml", "gateway.yml", "gateway.json")
	_, cc, err := config.LoadGateway(path, cmd.Flags())
	if err != nil {
		return gateway.GatewayRuntimeConfig{}, err
	}
	logging.Init(cc.Logging.Level)
	return cc, nil
}

// buildClient constructs a connected client from the config, layering the given
// functional options on top of the config-derived connection settings.
func buildClient(cmd *cobra.Command, cc gateway.GatewayRuntimeConfig, extra ...sdk.Option) (*sdk.Client, error) {
	opts := append([]sdk.Option{
		sdk.WithURI(cc.Dao.Mongo.URI),
		sdk.WithMaxElapsedTime(cc.Dao.Store.MaxElapsedTime),
	}, extra...)
	return sdk.New(cmd.Context(), opts...)
}

// connectClient loads the config via the given loader and returns a connected
// client. It is the common path for commands that need a plain client.
func connectClient(cmd *cobra.Command, load clientLoader, extra ...sdk.Option) (gateway.GatewayRuntimeConfig, *sdk.Client, error) {
	cc, err := load(cmd)
	if err != nil {
		return gateway.GatewayRuntimeConfig{}, nil, err
	}
	c, err := buildClient(cmd, cc, extra...)
	if err != nil {
		return cc, nil, err
	}
	return cc, c, nil
}
