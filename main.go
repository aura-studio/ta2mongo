// Command tango is a single binary with two role-oriented commands:
// standalone (tail TA logs and report to MongoDB) and gateway (an HTTP face
// for the same log-reporting functions).
package main

import (
	"os"

	"github.com/spf13/cobra"

	gatewaycmd "rocket-nano/tools/tango/cmd/gateway"
	standalonecmd "rocket-nano/tools/tango/cmd/standalone"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "tango",
		Short: "Tango: report ThinkingData logs to MongoDB (standalone or via HTTP gateway)",
	}
	root.PersistentFlags().String("config", "",
		"path to config file (.yaml/.yml/.json); default: <role>.{yaml,yml,json} next to the binary; skipped if absent")

	root.AddCommand(
		standalonecmd.NewCommand(),
		gatewaycmd.NewCommand(),
	)
	return root
}
