// Package client implements the legacy `tango client` command tree, kept as a
// compatibility wrapper. New deployments should use `tango operator` for
// one-shot commands or `tango gateway serve` for the HTTP face.
package client

import (
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/cmd/gateway"
	"rocket-nano/tools/tango/cmd/operator"
)

// NewCommand builds the legacy `tango client` parent command. It reuses the
// operator one-shot subcommands and the gateway serve subcommand, emitting a
// deprecation notice that points at the role-oriented replacements.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Legacy client role: use tango operator or tango gateway instead",
	}
	cmd.Run = func(cmd *cobra.Command, _ []string) {
		cmd.PrintErrln("warning: 'tango client' is deprecated; use 'tango operator' for one-shot commands or 'tango gateway serve' for HTTP")
		_ = cmd.Help()
	}
	// Viper-native hierarchical overrides: the flag name is the full config key.
	cmd.PersistentFlags().String("mongo.uri", "", "MongoDB connection URI (config key mongo.uri)")
	cmd.PersistentFlags().String("logging.level", "", "log level: debug, info, warn, error (config key logging.level)")
	cmd.AddCommand(append(operator.Subcommands(), gateway.ServeCommand())...)
	return cmd
}
