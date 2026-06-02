// Command tango is a single binary with role-oriented commands: report,
// worker, gateway, and operator.
package main

import (
	"os"

	"github.com/spf13/cobra"

	gatewaycmd "rocket-nano/tools/tango/cmd/gateway"
	operatorcmd "rocket-nano/tools/tango/cmd/operator"
	reportcmd "rocket-nano/tools/tango/cmd/report"
	workercmd "rocket-nano/tools/tango/cmd/worker"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "tango",
		Short: "Tango: role-oriented data pipeline for TA logs, tasks, and HTTP operations",
	}
	root.PersistentFlags().String("config", "",
		"path to config file (.yaml/.yml/.json); default: <role>.{yaml,yml,json} next to the binary; skipped if absent")

	root.AddCommand(
		reportcmd.NewCommand(),
		workercmd.NewCommand(),
		gatewaycmd.NewCommand(),
		operatorcmd.NewCommand(),
	)
	return root
}
