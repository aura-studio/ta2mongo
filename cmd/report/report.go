// Package report implements the `tango report` command: the report service —
// tail TA logs, filter, resolve identity, and write to MongoDB, with optional
// remote-config filter hot-reload.
package report

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/cmd/shared"
	"rocket-nano/tools/tango/internal/core/cli"
)

// NewCommand builds the `tango report` parent command.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report",
		Short: "Report service: tail TA logs, filter, and write to MongoDB",
	}
	cmd.PersistentFlags().String("runtime.mongo.uri", "", "MongoDB connection URI (config key runtime.mongo.uri)")
	cmd.PersistentFlags().String("runtime.logging.level", "", "log level: debug, info, warn, error (config key runtime.logging.level)")
	cmd.PersistentFlags().Bool("remoteConfig.enabled", false, "enable report remote-config hot reload (config key remoteConfig.enabled)")
	cmd.AddCommand(newRunCmd())
	return cmd
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the report service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := cli.ResolveConfigPath(shared.ConfigFlag(cmd),
				"report.yaml", "report.yml", "report.json")
			return shared.RunReportService(cmd, path)
		},
	}
}
