// Command tango is the single tango binary. In v1.0.0 only the daemon role is
// exposed; its mode is selected by the subcommand:
//
//   - tango daemon standalone — daemon role, standalone mode: tail TA logs →
//     report filter → MongoDB (local filter, no remote config, no tasks).
//   - tango daemon agent      — daemon role, agent mode: report + remote-config
//     sync + claim/execute tasks (report-sync / backfill / sql).
//
// The client role (string/file upload, backfill, ad-hoc SQL, task publishing,
// and the HTTP/REST server) is implemented in cmd/client but its entry point is
// intentionally NOT wired up for the v1.0.0 release — re-add
// clientcmd.NewCommand() below to enable it.
//
// This root only wires the shared --config flag and assembles the subcommand
// packages; the behaviour lives in cmd/daemon (and cmd/client when enabled).
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
	// --config selects the config file (a file path, not a config key). When
	// empty each subcommand auto-detects its own default next to the binary
	// (standalone.yaml / agent.yaml / client.yaml).
	//
	// Every other override is a viper-native hierarchical flag named exactly
	// after its config key (e.g. --generic.mongo.uri, --agent.instanceID,
	// --mongo.uri), declared on the owning subcommand — see cmd/daemon and
	// cmd/client.
	root.PersistentFlags().String("config", "",
		"path to config file (.yaml/.yml/.json); default: <mode>.{yaml,yml,json} next to the binary; skipped if absent")

	root.AddCommand(daemoncmd.NewCommand(), clientcmd.NewCommand())
	return root
}
