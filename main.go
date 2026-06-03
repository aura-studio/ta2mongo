// Command tango is a single binary with role-oriented commands.
package main

import (
	"os"

	"github.com/spf13/cobra"

	apicmd "rocket-nano/tools/tango/cmd/api"
	clicmd "rocket-nano/tools/tango/cmd/cli"
	daemoncmd "rocket-nano/tools/tango/cmd/daemon"
	gatewaycmd "rocket-nano/tools/tango/cmd/gateway"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "tango",
		Short: "Tango: role-oriented ThinkingData log processing",
	}
	root.PersistentFlags().String("config", "",
		"path to config file (.yaml/.yml/.json); default: <role>.{yaml,yml,json} next to the binary; skipped if absent")

	root.AddCommand(
		apicmd.NewCommand(),
		clicmd.NewCommand(),
		daemoncmd.NewCommand(),
		gatewaycmd.NewCommand(),
	)
	return root
}
