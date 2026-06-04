// Package cli implements the `tango cli` command: read TA log lines from stdin
// and ingest them through one of the three strategies (single/batch/pipeline).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/process"
	clirole "rocket-nano/tools/tango/internal/role/cli"
)

// NewCommand builds the `tango cli` command. It reads newline-delimited TA JSON
// log lines from stdin and ingests them, printing the run stats as JSON.
func NewCommand() *cobra.Command {
	var mode string
	cmd := &cobra.Command{
		Use:   "cli",
		Short: "Read TA log lines from stdin and ingest them (single/batch/pipeline)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := resolveConfigPath(configFlag(cmd), "cli.yaml", "cli.yml", "cli.json")
			c, err := config.Load(path, cmd.Flags())
			if err != nil {
				return err
			}
			logging.Init(c.Logging.Level)

			m, err := process.ParseMode(mode)
			if err != nil {
				return err
			}

			res, err := clirole.Run(cmd.Context(), c.Dao, c.Process, c.Parser.Filter, m, cmd.InOrStdin())
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		},
	}
	// --mode is a runtime argument (which strategy to use for this run), not a
	// config key. Every config key is also exposed as a --<key> flag below.
	cmd.Flags().StringVar(&mode, "mode", "batch", "upload mode: single | batch | pipeline")
	config.RegisterFlags(cmd.Flags())
	return cmd
}

// configFlag reads the inherited --config persistent flag (empty when unset).
func configFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("config")
	return v
}

// resolveConfigPath returns the config file path to use. When flagVal is set
// (the --config flag) it is returned verbatim. Otherwise the first of the
// candidate filenames that exists in the binary's own directory is returned.
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
