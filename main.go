// Command tango is a single binary with role-oriented commands. The primary
// commands are report, worker, gateway, and operator; daemon/client remain as
// compatibility wrappers for older deployments.
package main

import (
	"os"

	"github.com/spf13/cobra"

	clientcmd "rocket-nano/tools/tango/cmd/client"
	daemoncmd "rocket-nano/tools/tango/cmd/daemon"
	gatewaycmd "rocket-nano/tools/tango/cmd/gateway"
	operatorcmd "rocket-nano/tools/tango/cmd/operator"
	profilecmd "rocket-nano/tools/tango/cmd/profile"
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
		"path to config file (.yaml/.yml/.json); default: <mode>.{yaml,yml,json} next to the binary; skipped if absent")

	root.AddCommand(
		// Role-oriented primary commands.
		reportcmd.NewCommand(),
		workercmd.NewCommand(),
		gatewaycmd.NewCommand(),
		operatorcmd.NewCommand(),
		profilecmd.NewCommand(),
		// Legacy compatibility wrappers.
		daemoncmd.NewCommand(),
		clientcmd.NewCommand(),
	)
	return root
}
