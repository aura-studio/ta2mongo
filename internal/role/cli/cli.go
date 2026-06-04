// Package cli implements the command-line role: console-driven ingestion. It
// reads log lines from an input stream (stdin) and ingests them through the
// embedded api engine, using the same process.mode strategy as the gateway and
// api roles.
package cli

import (
	"context"
	"io"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/role/api"
	"github.com/aura-studio/tango/internal/source"
)

// Run reads log lines from in (the console's stdin) and ingests them with
// procCfg.Mode, returning per-run statistics. It builds an api engine, ensures
// indexes, runs the stdin source to completion, and closes the engine.
func Run(ctx context.Context, daoCfg *dao.Config, procCfg *process.Config, parserCfg *parser.Config, in io.Reader) (api.Result, error) {
	eng, err := api.New(ctx, daoCfg, procCfg, parserCfg)
	if err != nil {
		return api.Result{}, err
	}
	defer eng.Close()

	if err := eng.EnsureIndexes(ctx); err != nil {
		return api.Result{}, err
	}
	return eng.Run(ctx, source.NewReader(in))
}
