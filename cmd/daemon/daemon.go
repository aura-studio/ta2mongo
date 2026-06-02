// Package daemon implements the legacy `tango daemon` command tree
// (standalone / agent), kept as a compatibility wrapper. New deployments should
// use `tango report run`, `tango worker run`, or `tango profile managed`.
package daemon

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/cmd/shared"
	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/cli"
)

// NewCommand builds the legacy `tango daemon` parent command with its two run
// modes, standalone and agent. Both emit a deprecation notice pointing at the
// role-oriented replacements.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Legacy daemon role: use tango report, worker, or profile instead",
	}
	cmd.Run = func(cmd *cobra.Command, _ []string) {
		cmd.PrintErrln("warning: 'tango daemon' is deprecated; use 'tango report run', 'tango worker run', or 'tango profile managed'")
		_ = cmd.Help()
	}
	// Viper-native hierarchical overrides shared by both modes: the flag name is
	// the full config key.
	cmd.PersistentFlags().String("generic.mongo.uri", "", "MongoDB connection URI (config key generic.mongo.uri)")
	cmd.PersistentFlags().String("generic.logging.level", "", "log level: debug, info, warn, error (config key generic.logging.level)")
	cmd.AddCommand(newStandaloneCmd(), newAgentCmd())
	return cmd
}

func newStandaloneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "standalone",
		Short: "Standalone reporting: tail TA logs into MongoDB (local filter, no remote config, no tasks)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.PrintErrln("warning: 'tango daemon standalone' is deprecated; use 'tango report run'")
			path := cli.ResolveConfigPath(shared.ConfigFlag(cmd), "standalone.yaml", "standalone.yml", "standalone.json")
			return shared.RunDaemon(cmd, config.DaemonModeStandalone, path)
		},
	}
}

func newAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent: report + remote-config sync + claim/execute tasks (report-sync / backfill / sql)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.PrintErrln("warning: 'tango daemon agent' is deprecated; use separate 'tango report run' and 'tango worker run', or 'tango profile managed'")
			path := cli.ResolveConfigPath(shared.ConfigFlag(cmd), "agent.yaml", "agent.yml", "agent.json")
			return shared.RunDaemon(cmd, config.DaemonModeAgent, path)
		},
	}
	cmd.Flags().String("agent.instanceID", "", "agent instance id (config key agent.instanceID; required)")
	return cmd
}
