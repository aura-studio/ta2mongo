// Package worker implements the `tango worker` command: the task worker
// service — claim and execute report-sync / backfill / sql tasks from the
// shared MongoDB task queue.
package worker

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/cmd/shared"
	"rocket-nano/tools/tango/internal/core/cli"
)

// NewCommand builds the `tango worker` parent command.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "Task worker service: claim and execute report-sync, backfill, and sql tasks",
	}
	cmd.PersistentFlags().String("mongo.uri", "", "MongoDB connection URI (config key mongo.uri)")
	cmd.PersistentFlags().String("logging.level", "", "log level: debug, info, warn, error (config key logging.level)")
	cmd.PersistentFlags().String("instanceID", "", "worker instance id (alias for agent.instanceID; required)")
	cmd.PersistentFlags().String("agent.instanceID", "", "worker instance id (config key agent.instanceID; required)")
	cmd.AddCommand(newRunCmd())
	return cmd
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the task worker service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := cli.ResolveConfigPath(shared.ConfigFlag(cmd),
				"worker.yaml", "worker.yml", "worker.json",
				"agent.yaml", "agent.yml", "agent.json")
			return shared.RunWorkerService(cmd, path)
		},
	}
}
