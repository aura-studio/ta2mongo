// Package standalone implements the `tango standalone` command: the report
// service — tail TA logs, filter, resolve identity, and write to MongoDB.
package standalone

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/cmd/shared"
)

// NewCommand builds the `tango standalone` command. It tails the configured TA
// logs and reports them to MongoDB, running until interrupted.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "standalone",
		Short: "Standalone report service: tail TA logs, filter, and write to MongoDB",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := shared.ResolveConfigPath(shared.ConfigFlag(cmd),
				"standalone.yaml", "standalone.yml", "standalone.json")
			return shared.RunStandaloneService(cmd, path)
		},
	}
	cmd.Flags().String("runtime.mongo.uri", "", "MongoDB connection URI (config key runtime.mongo.uri)")
	cmd.Flags().String("runtime.logging.level", "", "log level: debug, info, warn, error (config key runtime.logging.level)")
	return cmd
}
