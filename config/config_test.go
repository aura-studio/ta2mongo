package config

import (
	"os"
	"testing"
	"time"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/parser"
)

// ---------------------------------------------------------------------------
// source.tailer.rescanInterval
// ---------------------------------------------------------------------------

func TestRescanInterval_Default(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
source:
  tailer:
    logPattern:
      - "/tmp/.*\\.log"
`)
	if got, want := srcCfg(t, tree).Tailer.RescanInterval, 30*time.Second; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_CustomValue(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
source:
  tailer:
    rescanInterval: "60s"
`)
	if got, want := srcCfg(t, tree).Tailer.RescanInterval, 60*time.Second; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_ZeroFallsBackToDefault(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
source:
  tailer:
    rescanInterval: "0s"
`)
	if got, want := srcCfg(t, tree).Tailer.RescanInterval, 30*time.Second; got != want {
		t.Errorf("RescanInterval = %v, want %v (should fallback for zero)", got, want)
	}
}

// ---------------------------------------------------------------------------
// dao.store.maxElapsedTime
// ---------------------------------------------------------------------------

func TestMaxElapsedTime_Default(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if got, want := daoCfg(t, tree).Store.MaxElapsedTime, 10*time.Second; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_CustomValue(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
  store:
    maxElapsedTime: "30s"
`)
	if got, want := daoCfg(t, tree).Store.MaxElapsedTime, 30*time.Second; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_FallbackForZero(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
  store:
    maxElapsedTime: "0s"
`)
	if got, want := daoCfg(t, tree).Store.MaxElapsedTime, 10*time.Second; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v (should fallback for zero)", got, want)
	}
}

// ---------------------------------------------------------------------------
// Per-module validation (each FromTree validates its own branch)
// ---------------------------------------------------------------------------

func TestValidation_MissingMongoURI(t *testing.T) {
	tree := treeFromYAML(t, `
source:
  tailer:
    logPattern:
      - "/tmp/.*\\.log"
`)
	if _, err := dao.FromTree(tree); err == nil {
		t.Fatal("expected error for missing dao.mongo.uri")
	}
}

func TestValidation_MissingLogPattern_OK(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if _, err := dao.FromTree(tree); err != nil {
		t.Fatalf("unexpected dao error: %v", err)
	}
	// An empty tailer log pattern is a valid source config (daemon checks it
	// separately at run time), so source.FromTree must not fail.
	if _, err := parser.FromTree(tree); err != nil {
		t.Fatalf("unexpected parser error: %v", err)
	}
	_ = srcCfg(t, tree)
}

// ---------------------------------------------------------------------------
// Filter expressions (parser.filter.*)
// ---------------------------------------------------------------------------

func TestFilter_LoadsFromYAML(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
parser:
  filter:
    include:
      - '#type == "track"'
      - '#type == "user_set"'
    exclude:
      - 'debug == true'
`)
	f := parserCfg(t, tree).Filter
	if len(f.Include) != 2 {
		t.Errorf("Filter.Include len = %d, want 2", len(f.Include))
	}
	if len(f.Exclude) != 1 {
		t.Errorf("Filter.Exclude len = %d, want 1", len(f.Exclude))
	}
}

func TestFilter_ValidationFailsForBadExpression(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
parser:
  filter:
    include:
      - '#type =='
`)
	if _, err := parser.FromTree(tree); err == nil {
		t.Fatal("expected parser.FromTree to fail for malformed expression")
	}
}

func TestFilter_EmptyByDefault(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	f := parserCfg(t, tree).Filter
	if len(f.Include) != 0 || len(f.Exclude) != 0 {
		t.Errorf("expected empty filter lists, got inc=%v exc=%v", f.Include, f.Exclude)
	}
}

// ---------------------------------------------------------------------------
// process.* knobs
// ---------------------------------------------------------------------------

func TestFlushInterval(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
process:
  pipeline:
    flushInterval: "500ms"
`)
	if got, want := procCfg(t, tree).Pipeline.FlushInterval, 500*time.Millisecond; got != want {
		t.Errorf("FlushInterval = %v, want %v", got, want)
	}
}

func TestNestedKnobs_Defaults(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if got := daoCfg(t, tree).Mongo.ConnectTimeout; got != 10*time.Second {
		t.Errorf("connectTimeout = %v", got)
	}
	src := srcCfg(t, tree)
	if src.Tailer.PollInterval != 200*time.Millisecond {
		t.Errorf("pollInterval = %v", src.Tailer.PollInterval)
	}
	if src.Tailer.MaxLineBytes != 10*1024*1024 {
		t.Errorf("maxLineBytes = %d", src.Tailer.MaxLineBytes)
	}
	if got := procCfg(t, tree).Pipeline.DeadLetterCap; got != 128 {
		t.Errorf("deadLetterCap = %d", got)
	}
	lc := logCfg(t, tree)
	if lc.Level != "info" || lc.Format != "text" {
		t.Errorf("logging = %+v", lc)
	}
}

func TestChannelBuffer_DerivedVsExplicit(t *testing.T) {
	tree := treeFromYAML(t, "dao:\n  mongo:\n    uri: x\nprocess:\n  pipeline:\n    batchSize: 500\n")
	if got := procCfg(t, tree).Pipeline.ChannelSize(); got != 1000 {
		t.Errorf("derived channel size = %d, want 1000", got)
	}
	tree = treeFromYAML(t, "dao:\n  mongo:\n    uri: x\nprocess:\n  pipeline:\n    batchSize: 500\n    channelBuffer: 4096\n")
	if got := procCfg(t, tree).Pipeline.ChannelSize(); got != 4096 {
		t.Errorf("explicit channel size = %d, want 4096", got)
	}
}

func TestBatchSizeMinMax_AutoDerivation(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
process:
  pipeline:
    batchSize: 1000
`)
	p := procCfg(t, tree).Pipeline
	if got := p.MinBatchSize(); got != 250 {
		t.Errorf("MinBatchSize() = %d, want 250", got)
	}
	if got := p.MaxBatchSize(); got != 2000 {
		t.Errorf("MaxBatchSize() = %d, want 2000", got)
	}
}

// ---------------------------------------------------------------------------
// role.gateway.*
// ---------------------------------------------------------------------------

func TestGatewayDefaults(t *testing.T) {
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if got := roleCfg(t, tree).Gateway.Addr; got != ":8080" {
		t.Errorf("role.gateway.addr default = %q, want :8080", got)
	}
	if got := procCfg(t, tree).Mode; got != "batch" {
		t.Errorf("process.mode default = %q, want batch", got)
	}
}

func TestGatewayLoadsFromYAML(t *testing.T) {
	// role.gateway holds only gateway-specific knobs (addr); the upload
	// processing/filter come from the shared top-level process.* /
	// parser.filter.* modules.
	tree := treeFromYAML(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
process:
  mode: pipeline
  batchSize: 250
  pipeline:
    batchWorkers: 4
role:
  gateway:
    addr: ":9090"
`)
	if got := roleCfg(t, tree).Gateway.Addr; got != ":9090" {
		t.Errorf("addr = %q", got)
	}
	pc := procCfg(t, tree)
	if pc.Mode != "pipeline" {
		t.Errorf("process.mode = %q", pc.Mode)
	}
	if pc.BatchSize != 250 {
		t.Errorf("process.batchSize = %d", pc.BatchSize)
	}
	if pc.Pipeline.BatchWorkers != 4 {
		t.Errorf("process.pipeline.batchWorkers = %d", pc.Pipeline.BatchWorkers)
	}
}

// ---------------------------------------------------------------------------
// ENV override via the Load path: TANGO_* overrides the YAML file.
// ---------------------------------------------------------------------------

func TestEnvOverridesYAML(t *testing.T) {
	os.Setenv("TANGO_LOGGING_LEVEL", "debug")
	os.Setenv("TANGO_SOURCE_TAILER_RESCANINTERVAL", "60s")
	defer func() {
		os.Unsetenv("TANGO_LOGGING_LEVEL")
		os.Unsetenv("TANGO_SOURCE_TAILER_RESCANINTERVAL")
	}()

	tree := treeFromYAML(t, `
logging:
  level: "error"
dao:
  mongo:
    uri: "mongodb://localhost"
source:
  tailer:
    logPattern: ["/tmp/x.log"]
    rescanInterval: "30s"
`)
	if got := logCfg(t, tree).Level; got != "debug" {
		t.Errorf("logging.level = %q, want debug (ENV should override YAML)", got)
	}
	if got := srcCfg(t, tree).Tailer.RescanInterval; got != 60*time.Second {
		t.Errorf("rescanInterval = %v, want 60s (ENV should override YAML)", got)
	}
}
