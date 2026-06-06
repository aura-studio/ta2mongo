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

// RunData reads a single Extended-JSON Mongo Data API request from in, executes
// it through the embedded api engine, and writes the Extended-JSON response to
// out. It is the console equivalent of the gateway POST /data endpoint and does
// not use the process/parser config.
func RunData(ctx context.Context, daoCfg *dao.Config, in io.Reader, out io.Writer) error {
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	req, err := dao.DecodeDataRequest(body)
	if err != nil {
		return err
	}

	eng, err := api.New(ctx, daoCfg, nil, nil)
	if err != nil {
		return err
	}
	defer eng.Close()

	resp, err := eng.Data(ctx, req)
	if err != nil {
		return err
	}
	encoded, err := resp.MarshalEJSON()
	if err != nil {
		return err
	}
	if _, err := out.Write(encoded); err != nil {
		return err
	}
	_, err = out.Write([]byte("\n"))
	return err
}
