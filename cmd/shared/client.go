// Package shared holds the glue used by the role command packages (standalone,
// gateway): config-file resolution, client construction, and the long-running
// standalone service runner. The cmd packages stay thin by delegating; argument
// parsing aside, all wiring lives here.
package shared

import (
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	sdk "rocket-nano/tools/tango/client"
	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/cli"
)

// ConfigFlag reads the inherited --config persistent flag (empty when unset).
func ConfigFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("config")
	return v
}

// ClientLoader resolves and loads the ClientConfig a client-driven command runs
// on, returning a ready-to-use ClientConfig and a logger derived from its
// logging level.
type ClientLoader func(cmd *cobra.Command) (config.ClientConfig, *logrus.Logger, error)

// GatewayConfig loads gateway.{yaml,yml,json} via the unified RoleConfig schema.
func GatewayConfig(cmd *cobra.Command) (config.ClientConfig, *logrus.Logger, error) {
	path := cli.ResolveConfigPath(ConfigFlag(cmd), "gateway.yaml", "gateway.yml", "gateway.json")
	_, cc, err := config.LoadGateway(path, cmd.Flags())
	if err != nil {
		return config.ClientConfig{}, nil, err
	}
	return cc, cli.NewLogger(cc.Logging.Level), nil
}

// BuildClient constructs a connected client from the config, layering the given
// functional options on top of the config-derived connection settings.
func BuildClient(cmd *cobra.Command, cc config.ClientConfig, logger *logrus.Logger, extra ...sdk.Option) (*sdk.Client, error) {
	opts := append([]sdk.Option{
		sdk.WithURI(cc.Mongo.URI),
		sdk.WithMaxElapsedTime(cc.Mongo.MaxElapsedTime),
		sdk.WithLogger(logger),
	}, extra...)
	return sdk.New(cmd.Context(), opts...)
}

// ConnectClient loads the config via the given loader and returns a connected
// client. It is the common path for commands that need a plain client.
func ConnectClient(cmd *cobra.Command, load ClientLoader, extra ...sdk.Option) (config.ClientConfig, *sdk.Client, *logrus.Logger, error) {
	cc, logger, err := load(cmd)
	if err != nil {
		return config.ClientConfig{}, nil, nil, err
	}
	c, err := BuildClient(cmd, cc, logger, extra...)
	if err != nil {
		return cc, nil, nil, err
	}
	return cc, c, logger, nil
}
