package cli

import (
	"context"
	"encoding/json"
	"os"

	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
)

// Role is the cli runtime role (role.mode = cli): the console equivalent of the
// gateway /upload — read TA log lines from stdin, ingest them with the configured
// process.mode, and print the run statistics as JSON to stdout.
type Role struct{}

// Run slices the dao / process / parser branches from cfg, ingests stdin through
// the embedded engine via the package-level Run, and writes the result as
// indented JSON to stdout.
func (Role) Run(ctx context.Context, cfg cfgtree.Tree) error {
	daoCfg, err := dao.FromTree(cfg)
	if err != nil {
		return err
	}
	procCfg, err := process.FromTree(cfg)
	if err != nil {
		return err
	}
	parserCfg, err := parser.FromTree(cfg)
	if err != nil {
		return err
	}

	res, err := Run(ctx, daoCfg, procCfg, parserCfg, os.Stdin)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
