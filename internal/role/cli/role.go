package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/source"
)

// Role is the cli runtime role (role.mode = cli). With role.cli.function=upload
// (the default) it is the console equivalent of the gateway /upload — read TA log
// lines from stdin, ingest them with the configured process.mode, and print the
// run statistics as JSON to stdout. With role.cli.function=file it bulk
// imports the explicitly-listed on-disk files in source.file.paths once
// (no glob, no directories, no checkpoint; re-runs re-import everything — events
// and user_set-style ops converge, accumulating user_add/user_append re-apply)
// and prints the same
// statistics. With role.cli.function=ejson it is the console
// equivalent of POST /ejson — read one Extended-JSON Mongo Data API request from
// stdin and write the Extended-JSON response to stdout. With
// role.cli.function=sql it is the console equivalent of POST /sql — read one SQL
// statement from stdin and write the Extended-JSON result to stdout.
type Role struct{}

// Run slices the role.cli config and dispatches to the upload or data function.
func (Role) Run(ctx context.Context, cfg cfgtree.Tree) error {
	daoCfg, err := dao.FromTree(cfg)
	if err != nil {
		return err
	}

	var cliCfg Config
	if err := cfg.Sub("role").Sub("cli").Into(&cliCfg); err != nil {
		return err
	}
	cliCfg.ApplyDefaults()
	if err := cliCfg.Validate(); err != nil {
		return err
	}

	if cliCfg.Function == FunctionEJSON {
		return RunEJSON(ctx, daoCfg, os.Stdin, os.Stdout)
	}
	if cliCfg.Function == FunctionSQL {
		return RunSQL(ctx, daoCfg, os.Stdin, os.Stdout)
	}
	if cliCfg.Function == FunctionConfig {
		cfgsyncCfg, err := cfgsync.FromTree(cfg)
		if err != nil {
			return err
		}
		return RunConfig(ctx, daoCfg, cfgsyncCfg, cliCfg.ConfigMode, os.Stdin, os.Stdout)
	}
	if cliCfg.Function == FunctionConfigGet {
		cfgsyncCfg, err := cfgsync.FromTree(cfg)
		if err != nil {
			return err
		}
		return RunConfigGet(ctx, daoCfg, cfgsyncCfg, os.Stdout)
	}

	procCfg, err := process.FromTree(cfg)
	if err != nil {
		return err
	}
	parserCfg, err := parser.FromTree(cfg)
	if err != nil {
		return err
	}

	if cliCfg.Function == FunctionFile {
		srcCfg, err := source.FromTree(cfg)
		if err != nil {
			return err
		}
		// Fail fast before connecting Mongo.
		if len(srcCfg.File.Paths) == 0 {
			return fmt.Errorf("cli: function=file requires source.file.paths")
		}
		res, err := RunFile(ctx, daoCfg, procCfg, parserCfg, srcCfg.File)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	res, err := RunUpload(ctx, daoCfg, procCfg, parserCfg, os.Stdin)
	if err != nil {
		return err
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(res)
}
