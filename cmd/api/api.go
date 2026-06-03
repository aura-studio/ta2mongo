// Package api implements the `tango api` command slot.
package api

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewCommand builds the `tango api` command. The API role is reserved for
// embedded/programmatic use and does not have runtime behavior yet.
func NewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "api",
		Short: "API role (reserved)",
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("api role is not implemented yet")
		},
	}
}
