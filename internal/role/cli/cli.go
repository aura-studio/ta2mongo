// Package cli implements the command-line role: console-driven ingestion. It
// reads log lines from an input stream (stdin) and ingests them through the
// embedded api engine, supporting the same three strategies as the gateway and
// api roles (single / batch / pipeline).
package cli

import (
	"context"
	"io"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role/api"
	"rocket-nano/tools/tango/internal/source/stdin"
)

// Run reads log lines from in (the console's stdin) and ingests them with the
// given mode, returning per-run statistics. It builds an api engine, ensures
// indexes, runs the stdin source to completion, and closes the engine.
func Run(ctx context.Context, daoCfg *dao.Config, procCfg *process.Config, filterCfg *filter.Config, mode process.Mode, in io.Reader) (api.Result, error) {
	eng, err := api.New(ctx, daoCfg, procCfg, filterCfg)
	if err != nil {
		return api.Result{}, err
	}
	defer eng.Close()

	if err := eng.EnsureIndexes(ctx); err != nil {
		return api.Result{}, err
	}
	return eng.Run(ctx, mode, stdin.New(in))
}
