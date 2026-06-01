// Command tango is the single tango binary. Its role is selected by the
// top-level subcommand, each implemented in its own cmd/* package:
//
//   - tango daemon standalone — daemon role, standalone mode: tail TA logs →
//     report filter → MongoDB (local filter, no remote config, no tasks).
//   - tango daemon agent      — daemon role, agent mode: report + remote-config
//     sync + claim/execute tasks (report-sync / backfill / sql).
//   - tango client <subcmd>   — client role: string upload, file upload,
//     backfill, ad-hoc SQL, task publishing — as CLI subcommands and an
//     HTTP/REST server (`tango client serve`).
//
// This root only wires the shared persistent flags and assembles the
// subcommand packages; the behaviour lives in cmd/daemon and cmd/client.
package main

import (
	"os"

	"github.com/spf13/cobra"

	clientcmd "rocket-nano/tools/tango/cmd/client"
	daemoncmd "rocket-nano/tools/tango/cmd/daemon"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "tango",
		Short: "Tango: daemon and client roles in one binary (select via the daemon/client subcommand)",
	}
	// Shared connection/logging overrides, inherited by every subcommand. When
	// --config is empty each subcommand auto-detects its own default config file
	// next to the binary (standalone.yaml / agent.yaml / client.yaml).
	root.PersistentFlags().String("config", "",
		"path to config file (.yaml/.yml/.json); default: <mode>.{yaml,yml,json} next to the binary; skipped if absent")
	root.PersistentFlags().String("mongoURI", "", "MongoDB connection URI (maps to mongo.uri)")
	root.PersistentFlags().String("logLevel", "", "log level: debug, info, warn, error")

	root.AddCommand(daemoncmd.NewCommand(), clientcmd.NewCommand())
	return root
}
