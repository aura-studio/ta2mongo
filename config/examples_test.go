package config

import (
	"path/filepath"
	"testing"

	"github.com/aura-studio/tango/internal/role"
	"github.com/aura-studio/tango/internal/role/cli"
)

// TestExampleConfigsLoad ensures every shipped example config (yaml + json,
// daemon + gateway + cli, max + min) loads and decodes cleanly: Load builds the
// tree and every module's FromTree slices + validates its own branch, so the
// examples never drift from the schema.
func TestExampleConfigsLoad(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "examples", "config", "*", "*.*"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, m := range matches {
		switch filepath.Ext(m) {
		case ".yaml", ".yml", ".json":
			files = append(files, m)
		}
	}
	if len(files) == 0 {
		t.Fatal("no example config files found")
	}
	for _, f := range files {
		f := f
		t.Run(filepath.Base(filepath.Dir(f))+"/"+filepath.Base(f), func(t *testing.T) {
			tree, err := Load(f, nil)
			if err != nil {
				t.Fatalf("Load(%s): %v", f, err)
			}
			// Every section must slice + validate cleanly.
			_ = logCfg(t, tree)
			_ = daoCfg(t, tree)
			_ = parserCfg(t, tree)
			_ = srcCfg(t, tree)
			_ = procCfg(t, tree)
			_ = roleCfg(t, tree)
		})
	}
}

// TestMaxConfigsAreComplete checks each *.max.* example populates exactly the
// schema sections its role (and, for cli, its function) actually uses — "max" is
// a full reference of the *used* paths, with unused sections intentionally
// omitted. Every role uses logging + dao.mongo; the rest is role/function
// specific.
func TestMaxConfigsAreComplete(t *testing.T) {
	maxes, _ := filepath.Glob(filepath.Join("..", "examples", "config", "*", "*.max.*"))
	if len(maxes) == 0 {
		t.Fatal("no *.max.* example files found")
	}
	for _, f := range maxes {
		ext := filepath.Ext(f)
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			continue
		}
		f := f
		t.Run(filepath.Base(filepath.Dir(f))+"/"+filepath.Base(f), func(t *testing.T) {
			tree, err := Load(f, nil)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}

			// Universal: every role initializes logging and connects to MongoDB.
			if logCfg(t, tree).Level == "" {
				t.Error("logging.level empty")
			}
			if daoCfg(t, tree).Mongo.URI == "" {
				t.Error("dao.mongo.uri empty")
			}

			// requireProcess / requireParser are the upload-pipeline sections.
			requireParser := func() {
				if len(parserCfg(t, tree).Filter.Include) == 0 {
					t.Error("parser.filter.include empty")
				}
			}
			requireProcess := func() {
				if procCfg(t, tree).Pipeline.BatchWorkers == 0 {
					t.Error("process.pipeline.batchWorkers unset")
				}
			}

			rc := roleCfg(t, tree)
			switch rc.Mode {
			case role.Daemon:
				requireParser()
				requireProcess()
				if len(srcCfg(t, tree).Tailer.LogPattern) == 0 {
					t.Error("source.tailer.logPattern empty")
				}
			case role.Gateway:
				requireParser()
				requireProcess()
				if rc.Gateway == nil || rc.Gateway.Addr == "" {
					t.Error("role.gateway.addr empty")
				}
			case role.CLI:
				if rc.Cli == nil || rc.Cli.Function == "" {
					t.Error("role.cli.function empty")
				}
				// Only the upload function uses the parser + process pipeline; the
				// ejson / sql functions use neither (they go straight to MongoDB).
				if rc.Cli != nil && rc.Cli.Function == cli.FunctionUpload {
					requireParser()
					requireProcess()
				}
			default:
				t.Errorf("unknown role.mode %q", rc.Mode)
			}
		})
	}
}
