// Package cli implements the `tango cli` command slot.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewCommand builds the `tango cli` command. The CLI role is reserved for
// one-shot command-line ingestion and does not have runtime behavior yet.
func NewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cli",
		Short: "CLI role (reserved)",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("cli role is not implemented yet")
		},
	}
}
