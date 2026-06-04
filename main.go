// Command tango is a single binary. The runtime role is selected by the config
// key role.mode (daemon / gateway / cli) — file / TANGO_ROLE_MODE / --role.mode —
// not by a subcommand.
package main

import (
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/role"
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
		Long: "Tango is a single binary. The runtime role is chosen by the config key " +
			"role.mode (daemon / gateway / cli), settable via the config file, " +
			"TANGO_ROLE_MODE, or --role.mode — there is no role subcommand.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE:         run,
	}
	root.PersistentFlags().String("config", "",
		"path to config file (.yaml/.yml/.json); default: tango.{yaml,yml,json} next to the binary; skipped if absent")
	// Every config key is also a --<key> flag (file / TANGO_* env / flag are
	// interchangeable), including --role.mode which selects the runtime role.
	config.RegisterFlags(root.Flags())
	return root
}

// run loads the config tree, configures logging, then dispatches to the role
// named by role.mode, handing it the whole tree to slice its own branches from.
func run(cmd *cobra.Command, _ []string) error {
	path := resolveConfigPath(configFlag(cmd), "tango.yaml", "tango.yml", "tango.json")
	tree, err := config.Load(path, cmd.Flags())
	if err != nil {
		return err
	}

	logCfg, err := logging.FromTree(tree)
	if err != nil {
		return err
	}
	logging.Init(logCfg)

	roleCfg, err := role.FromTree(tree)
	if err != nil {
		return err
	}
	r, err := role.Get(roleCfg.Mode)
	if err != nil {
		return err
	}
	return r.Run(cmd.Context(), tree)
}

// configFlag reads the --config persistent flag (empty when unset).
func configFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("config")
	return v
}

// resolveConfigPath returns the config file path to use. When flagVal is set
// (the --config flag) it is returned verbatim. Otherwise the first of the
// candidate filenames that exists in the binary's own directory is returned, so
// the binary auto-detects its default regardless of the working directory. When
// none exist it returns "" and the loader falls back to defaults + env + flags.
func resolveConfigPath(flagVal string, candidates ...string) string {
	if flagVal != "" {
		return flagVal
	}
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}
