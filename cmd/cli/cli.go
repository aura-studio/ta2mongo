// Package cli implements the `tango cli` command tree. Subcommands mirror the
// gateway HTTP paths, so `tango cli upload` is the console equivalent of
// POST /upload: read TA log lines from stdin and ingest them through one of the
// three strategies (single/batch/pipeline).
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
	clirole "rocket-nano/tools/tango/internal/role/cli"
)

// NewCommand builds the `tango cli` command tree. Its subcommands align with
// gateway paths rather than inventing a separate CLI surface.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cli",
		Short: "Console client for gateway-aligned operations",
	}
	cmd.AddCommand(newUploadCmd())
	return cmd
}

// newUploadCmd builds `tango cli upload`, the stdin-based equivalent of
// gateway POST /upload.
func newUploadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Read TA log lines from stdin and ingest them with process.mode",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := resolveConfigPath(configFlag(cmd), "cli.yaml", "cli.yml", "cli.json")
			c, err := config.Load(path, cmd.Flags())
			if err != nil {
				return err
			}
			logging.Init(c.Logging.Level)

			res, err := clirole.Run(cmd.Context(), c.Dao, c.Process, c.Parser.Filter, cmd.InOrStdin())
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		},
	}
	// Every config key is exposed as a --<key> flag. The upload strategy is
	// process.mode, so use --process.mode or the equivalent file/env setting.
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
