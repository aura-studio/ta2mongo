// Package cli implements the command-line role: console-driven ingestion. It
// reads log lines from an input stream (stdin) and ingests them through the
// embedded api engine, using the same process.mode strategy as the gateway and
// api roles.
package cli

import (
	"context"
	"encoding/json"
	"io"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/role/api"
	"github.com/aura-studio/tango/internal/source"
)

// RunUpload reads log lines from in (the console's stdin) and ingests them with
// procCfg.Mode, returning per-run statistics. It builds an api engine, ensures
// indexes, runs the stdin source to completion, and closes the engine. It backs
// the cli role's default function=upload.
func RunUpload(ctx context.Context, daoCfg *dao.Config, procCfg *process.Config, parserCfg *parser.Config, in io.Reader) (api.Result, error) {
	eng, err := api.New(ctx, daoCfg, procCfg, parserCfg, nil)
	if err != nil {
		return api.Result{}, err
	}
	defer eng.Close()

	if err := eng.EnsureIndexes(ctx); err != nil {
		return api.Result{}, err
	}
	return eng.Run(ctx, source.NewReader(in))
}

// RunEJSON reads a single Extended-JSON Mongo Data API request from in, executes
// it through the embedded api engine, and writes the Extended-JSON response to
// out. It is the console equivalent of the gateway POST /ejson endpoint and does
// not use the process/parser config.
func RunEJSON(ctx context.Context, daoCfg *dao.Config, in io.Reader, out io.Writer) error {
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	req, err := dao.DecodeEJSONRequest(body)
	if err != nil {
		return err
	}

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		return err
	}
	defer eng.Close()

	resp, err := eng.EJSON(ctx, req)
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

// RunSQL reads a single SQL statement from in, executes it through the embedded
// api engine (SQL Data API), and writes the Extended-JSON result to out. It is
// the console equivalent of the gateway POST /sql endpoint and does not use the
// process/parser config.
func RunSQL(ctx context.Context, daoCfg *dao.Config, in io.Reader, out io.Writer) error {
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		return err
	}
	defer eng.Close()

	res, err := eng.SQL(ctx, string(body))
	if err != nil {
		return err
	}
	encoded, err := res.MarshalEJSON()
	if err != nil {
		return err
	}
	if _, err := out.Write(encoded); err != nil {
		return err
	}
	_, err = out.Write([]byte("\n"))
	return err
}

// RunConfig reads a single runtime config document (JSON) from in, publishes it
// to the central cfgsync collection through the embedded api engine, and writes
// {"version":<new>} as JSON to out. It is the console equivalent of the gateway
// POST /config endpoint. cfgsyncCfg supplies the target collection / document id
// (so the cli publishes to the same document the daemon/gateway watch); it does
// not use the process/parser config.
func RunConfig(ctx context.Context, daoCfg *dao.Config, cfgsyncCfg *cfgsync.Config, in io.Reader, out io.Writer) error {
	body, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	var doc bson.M
	if err := json.Unmarshal(body, &doc); err != nil {
		return err
	}

	eng, err := api.New(ctx, daoCfg, nil, nil, cfgsyncCfg)
	if err != nil {
		return err
	}
	defer eng.Close()

	version, err := eng.PublishConfig(ctx, doc)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]int64{"version": version})
}
