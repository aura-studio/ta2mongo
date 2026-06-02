// Package profile implements the `tango profile` command: compatibility
// deployment profiles (local, managed) composed from the role services. These
// are convenience wrappers, not core roles.
package profile

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/cmd/shared"
	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/cli"
)

// NewCommand builds the `tango profile` parent command.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Compatibility deployment profiles composed from role services",
	}
	cmd.AddCommand(newLocalCmd(), newManagedCmd())
	return cmd
}

func newLocalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "local",
		Short: "Compatibility profile for report-only local deployment",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.PrintErrln("warning: 'tango profile local' is a compatibility profile; prefer 'tango report run'")
			path := cli.ResolveConfigPath(shared.ConfigFlag(cmd),
				"local.yaml", "local.yml", "local.json",
				"standalone.yaml", "standalone.yml", "standalone.json")
			return shared.RunDaemon(cmd, config.DaemonModeStandalone, path)
		},
	}
}

func newManagedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "managed",
		Short: "Compatibility profile for report + task worker in one process",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.PrintErrln("warning: 'tango profile managed' is a compatibility profile; prefer separate 'tango report run' and 'tango worker run' processes")
			path := cli.ResolveConfigPath(shared.ConfigFlag(cmd),
				"managed.yaml", "managed.yml", "managed.json",
				"agent.yaml", "agent.yml", "agent.json")
			return shared.RunDaemon(cmd, config.DaemonModeAgent, path)
		},
	}
	cmd.Flags().String("agent.instanceID", "", "agent instance id (config key agent.instanceID; required)")
	return cmd
}
