package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"

	"rocket-nano/tools/tango/internal/cfgtree"
	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role"
	"rocket-nano/tools/tango/internal/source"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// treeFromYAML writes y to a temp config file and loads it through the real Load
// path (defaults + file + TANGO_* env + flags), returning the config tree.
func treeFromYAML(t *testing.T, y string) cfgtree.Tree {
	t.Helper()
	tree, err := Load(writeFile(t, "tango.yaml", y), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// Per-section decode helpers: each slices its module branch from the tree,
// applying that module's own defaults + validation, and fails the test on error.
func daoCfg(t *testing.T, tr cfgtree.Tree) *dao.Config {
	t.Helper()
	c, err := dao.FromTree(tr)
	if err != nil {
		t.Fatalf("dao.FromTree: %v", err)
	}
	return c
}

func srcCfg(t *testing.T, tr cfgtree.Tree) *source.Config {
	t.Helper()
	c, err := source.FromTree(tr)
	if err != nil {
		t.Fatalf("source.FromTree: %v", err)
	}
	return c
}

func procCfg(t *testing.T, tr cfgtree.Tree) *process.Config {
	t.Helper()
	c, err := process.FromTree(tr)
	if err != nil {
		t.Fatalf("process.FromTree: %v", err)
	}
	return c
}

func parserCfg(t *testing.T, tr cfgtree.Tree) *parser.Config {
	t.Helper()
	c, err := parser.FromTree(tr)
	if err != nil {
		t.Fatalf("parser.FromTree: %v", err)
	}
	return c
}

func roleCfg(t *testing.T, tr cfgtree.Tree) *role.Config {
	t.Helper()
	c, err := role.FromTree(tr)
	if err != nil {
		t.Fatalf("role.FromTree: %v", err)
	}
	return c
}

func logCfg(t *testing.T, tr cfgtree.Tree) *logging.Config {
	t.Helper()
	c, err := logging.FromTree(tr)
	if err != nil {
		t.Fatalf("logging.FromTree: %v", err)
	}
	return c
}

func TestLoad_Unified(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost/report"
parser:
  filter:
    include: ['#type == "track"']
source:
  tailer:
    logPattern: ["/tmp/.*\\.log"]
`)
	if got := daoCfg(t, tree).Mongo.URI; got != "mongodb://localhost/report" {
		t.Errorf("dao.mongo.uri = %q", got)
	}
	if inc := parserCfg(t, tree).Filter.Include; len(inc) != 1 || inc[0] != `#type == "track"` {
		t.Errorf("parser.filter.include = %v", inc)
	}
	if len(srcCfg(t, tree).Tailer.LogPattern) != 1 {
		t.Errorf("source.tailer.logPattern = %v", srcCfg(t, tree).Tailer.LogPattern)
	}
}

func TestLoad_TypedEnvOverrides(t *testing.T) {
	// Environment variables arrive as strings; the tree decode must coerce them
	// into int fields (weak typing) as well as string / duration ones.
	os.Setenv("TANGO_PROCESS_PIPELINE_BATCHSIZE", "2500") // int
	defer os.Unsetenv("TANGO_PROCESS_PIPELINE_BATCHSIZE")
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost/report"
source:
  tailer:
    logPattern: ["/tmp/x.log"]
process:
  pipeline:
    batchSize: 1000
`)
	if got := procCfg(t, tree).Pipeline.BatchSize; got != 2500 {
		t.Errorf("process.pipeline.batchSize via int env = %d, want 2500", got)
	}
}

// TestFlagOverridesEnvAndFile verifies the three input paths are consistent and
// correctly ordered: a --<key> flag (registered for every key) overrides both
// the TANGO_* env var and the config file value.
func TestFlagOverridesEnvAndFile(t *testing.T) {
	os.Setenv("TANGO_LOGGING_LEVEL", "warn")
	defer os.Unsetenv("TANGO_LOGGING_LEVEL")

	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(flags)
	// every key is a flag
	if flags.Lookup("dao.mongo.uri") == nil {
		t.Fatal("expected --dao.mongo.uri flag to be registered")
	}
	if flags.Lookup("process.pipeline.batchWorkers") == nil {
		t.Fatal("expected --process.pipeline.batchWorkers flag to be registered")
	}
	if err := flags.Parse([]string{"--logging.level", "debug"}); err != nil {
		t.Fatal(err)
	}

	tree, err := Load(writeFile(t, "tango.yaml", `
dao:
  mongo:
    uri: "mongodb://localhost/x"
logging:
  level: error
`), flags)
	if err != nil {
		t.Fatal(err)
	}
	// flag (debug) beats env (warn) beats file (error)
	if got := logCfg(t, tree).Level; got != "debug" {
		t.Errorf("logging.level = %q, want debug (flag should win)", got)
	}
}

func TestLoad_Gateway(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost/gw"
process:
  mode: pipeline
  batchSize: 250
role:
  gateway:
    addr: ":9090"
`)
	if got := daoCfg(t, tree).Mongo.URI; got != "mongodb://localhost/gw" {
		t.Errorf("dao.mongo.uri = %q", got)
	}
	if got := roleCfg(t, tree).Gateway.Addr; got != ":9090" {
		t.Errorf("role.gateway.addr = %q", got)
	}
	pc := procCfg(t, tree)
	if pc.Mode != "pipeline" {
		t.Errorf("process.mode = %q", pc.Mode)
	}
	if pc.BatchSize != 250 {
		t.Errorf("process.batchSize = %d", pc.BatchSize)
	}
}
