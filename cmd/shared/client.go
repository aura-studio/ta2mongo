// Package shared holds the glue used by the role-oriented command packages
// (report, worker, gateway, operator, profile) and the legacy daemon/client
// wrappers: config-file resolution, client construction, and the long-running
// report/worker service runners. The cmd packages stay thin by delegating
// argument parsing aside, all wiring lives here.
package shared

import (
	"encoding/json"
	"fmt"

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

// ClientConfigPath resolves the client config file: --config if set, else the
// auto-detected client.{yaml,yml,json} next to the binary.
func ClientConfigPath(cmd *cobra.Command) string {
	return cli.ResolveConfigPath(ConfigFlag(cmd), "client.yaml", "client.yml", "client.json")
}

// LoadClientConfig loads the client config (file + env + flags).
func LoadClientConfig(cmd *cobra.Command) (config.ClientConfig, *logrus.Logger, error) {
	cc, err := config.LoadClient(ClientConfigPath(cmd), cmd.Flags())
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
		sdk.WithTaskQueue(cc.Publish.TasksCollection, cc.Publish.InstancesCollection, cc.Publish.InstanceTTL),
	}, extra...)
	return sdk.New(cmd.Context(), opts...)
}

// LoadClient is the common path for commands that need a plain client.
func LoadClient(cmd *cobra.Command, extra ...sdk.Option) (config.ClientConfig, *sdk.Client, *logrus.Logger, error) {
	cc, logger, err := LoadClientConfig(cmd)
	if err != nil {
		return config.ClientConfig{}, nil, nil, err
	}
	c, err := BuildClient(cmd, cc, logger, extra...)
	if err != nil {
		return cc, nil, nil, err
	}
	return cc, c, logger, nil
}

// PrintJSON pretty-prints v as JSON to stdout.
func PrintJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
