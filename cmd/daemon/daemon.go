// Package daemon implements the `tango daemon` command: the report service
// tails TA logs, filters, resolves identity, and writes to MongoDB.
package daemon

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/internal/cmdshared"
)

// NewCommand builds the `tango daemon` command. It tails the configured TA
// logs and reports them to MongoDB, running until interrupted.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Daemon report service: tail TA logs, filter, and write to MongoDB",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := cmdshared.ResolveConfigPath(cmdshared.ConfigFlag(cmd),
				"daemon.yaml", "daemon.yml", "daemon.json")
			return cmdshared.RunDaemonService(cmd, path)
		},
	}
	cmd.Flags().String("runtime.mongo.uri", "", "MongoDB connection URI (config key runtime.mongo.uri)")
	cmd.Flags().String("runtime.logging.level", "", "log level: debug, info, warn, error (config key runtime.logging.level)")
	return cmd
}
