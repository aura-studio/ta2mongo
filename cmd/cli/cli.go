// Package cli implements the cli runtime role (role.mode = cli): the console
// equivalent of gateway POST /upload — read TA log lines from stdin and ingest
// them through one of the three strategies (single/batch/pipeline, process.mode).
package cli

import (
	"encoding/json"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	clirole "rocket-nano/tools/tango/internal/role/cli"
)

// Run executes the cli role against an already-loaded config: it reads TA log
// lines from stdin, ingests them with process.mode, and prints the result as
// JSON. The runtime role is selected by config key role.mode, dispatched from main.
func Run(cmd *cobra.Command, c *config.Config) error {
	res, err := clirole.Run(cmd.Context(), c.Dao, c.Process, c.Parser.Filter, cmd.InOrStdin())
	if err != nil {
		return err
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
