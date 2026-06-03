// Package cmdshared holds the glue used by the role command packages (daemon,
// gateway): config-file resolution, client construction, and the long-running
// daemon service runner. The cmd packages stay thin by delegating; argument
// parsing aside, all wiring lives here.
package cmdshared

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
	sdk "rocket-nano/tools/tango/internal/role/gateway/client"
)

// ConfigFlag reads the inherited --config persistent flag (empty when unset).
func ConfigFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("config")
	return v
}

// ResolveConfigPath returns the config file path to use. When flagVal is set
// (the --config flag) it is returned verbatim. Otherwise the first of the
// candidate filenames that exists in the binary's own directory is returned, so
// each subcommand auto-detects its own default (e.g. report.yaml / worker.yaml /
// gateway.yaml / operator.yaml) regardless of the current working directory.
// When none exist it returns "" and the loader falls back to defaults + env + flags.
func ResolveConfigPath(flagVal string, candidates ...string) string {
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

// ClientLoader resolves and loads the GatewayRuntimeConfig a gateway-driven command runs
// on. It also initializes the shared logger from the config's logging level.
type ClientLoader func(cmd *cobra.Command) (config.GatewayRuntimeConfig, error)

// GatewayConfig loads gateway.{yaml,yml,json} via the unified RoleConfig schema
// and initializes the shared logger from its logging level.
func GatewayConfig(cmd *cobra.Command) (config.GatewayRuntimeConfig, error) {
	path := ResolveConfigPath(ConfigFlag(cmd), "gateway.yaml", "gateway.yml", "gateway.json")
	_, cc, err := config.LoadGateway(path, cmd.Flags())
	if err != nil {
		return config.GatewayRuntimeConfig{}, err
	}
	logging.Init(cc.Runtime.Logging.Level)
	return cc, nil
}

// BuildClient constructs a connected client from the config, layering the given
// functional options on top of the config-derived connection settings.
func BuildClient(cmd *cobra.Command, cc config.GatewayRuntimeConfig, extra ...sdk.Option) (*sdk.Client, error) {
	opts := append([]sdk.Option{
		sdk.WithURI(cc.Dao.Mongo.URI),
		sdk.WithMaxElapsedTime(cc.Dao.Store.MaxElapsedTime),
	}, extra...)
	return sdk.New(cmd.Context(), opts...)
}

// ConnectClient loads the config via the given loader and returns a connected
// client. It is the common path for commands that need a plain client.
func ConnectClient(cmd *cobra.Command, load ClientLoader, extra ...sdk.Option) (config.GatewayRuntimeConfig, *sdk.Client, error) {
	cc, err := load(cmd)
	if err != nil {
		return config.GatewayRuntimeConfig{}, nil, err
	}
	c, err := BuildClient(cmd, cc, extra...)
	if err != nil {
		return cc, nil, err
	}
	return cc, c, nil
}
