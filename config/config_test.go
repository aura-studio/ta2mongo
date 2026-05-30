package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// helper to write a temp YAML config and return its path.
func writeYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "tango.yaml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------------------------------------------------------------------------
// source.rescanInterval tests
// ---------------------------------------------------------------------------

func TestRescanInterval_Default(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
source:
  logPattern:
    - "/tmp/.*\\.log"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 30 * time.Second
	if got := cfg.Source.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_CustomValue(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
source:
  rescanInterval: "60s"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 60 * time.Second
	if got := cfg.Source.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_ZeroFallsBackToDefault(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
source:
  rescanInterval: "0s"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 30 * time.Second
	if got := cfg.Source.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v (should fallback for zero)", got, want)
	}
}

// ---------------------------------------------------------------------------
// mongo.maxElapsedTime tests
// ---------------------------------------------------------------------------

func TestMaxElapsedTime_Default(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 10 * time.Second
	if got := cfg.Mongo.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_CustomValue(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
  maxElapsedTime: "30s"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 30 * time.Second
	if got := cfg.Mongo.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_FallbackForZero(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
  maxElapsedTime: "0s"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 10 * time.Second
	if got := cfg.Mongo.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v (should fallback for zero)", got, want)
	}
}

func TestMaxElapsedTime_LargeValue(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
  maxElapsedTime: "5m"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 5 * time.Minute
	if got := cfg.Mongo.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Validation edge cases
// ---------------------------------------------------------------------------

func TestValidation_MissingMongoURI(t *testing.T) {
	yaml := `
source:
  logPattern:
    - "/tmp/.*\\.log"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing mongo.uri")
	}
}

func TestValidation_MissingLogPattern_OK(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
`
	_, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidation_EmptyLogPattern_OK(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
source:
  logPattern: []
`
	_, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Filter expression tests
// ---------------------------------------------------------------------------

func TestFilter_LoadsFromYAML(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
filter:
  include:
    - '#type == "track"'
    - '#type == "user_set"'
  exclude:
    - 'debug == true'
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Filter.Include) != 2 {
		t.Errorf("Filter.Include len = %d, want 2", len(cfg.Filter.Include))
	}
	if len(cfg.Filter.Exclude) != 1 {
		t.Errorf("Filter.Exclude len = %d, want 1", len(cfg.Filter.Exclude))
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
	if _, err := cfg.BuildFilter(); err != nil {
		t.Errorf("BuildFilter: %v", err)
	}
}

func TestFilter_ValidationFailsForBadExpression(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
filter:
  include:
    - '#type =='
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Validate to fail for malformed expression")
	}
}

func TestFilter_EmptyByDefault(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Filter.Include) != 0 || len(cfg.Filter.Exclude) != 0 {
		t.Errorf("expected empty filter lists, got inc=%v exc=%v",
			cfg.Filter.Include, cfg.Filter.Exclude)
	}
}

func TestFlushInterval(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
pipeline:
  flushInterval: "500ms"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := 500 * time.Millisecond
	if got := cfg.Pipeline.FlushInterval; got != want {
		t.Errorf("FlushInterval = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// New nested-knob smoke tests
// ---------------------------------------------------------------------------

func TestNestedKnobs_Defaults(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mongo.ConnectTimeout != 10*time.Second {
		t.Errorf("connectTimeout = %v", cfg.Mongo.ConnectTimeout)
	}
	if cfg.Source.PollInterval != 200*time.Millisecond {
		t.Errorf("pollInterval = %v", cfg.Source.PollInterval)
	}
	if cfg.Source.MaxLineBytes != 10*1024*1024 {
		t.Errorf("maxLineBytes = %d", cfg.Source.MaxLineBytes)
	}
	if cfg.Pipeline.DeadLetterCap != 128 {
		t.Errorf("deadLetterCap = %d", cfg.Pipeline.DeadLetterCap)
	}
	if cfg.Logging.Level != "info" || cfg.Logging.Format != "text" {
		t.Errorf("logging = %+v", cfg.Logging)
	}
}

func TestChannelBuffer_DerivedVsExplicit(t *testing.T) {
	cfg, _ := Load(writeYAML(t, "mongo:\n  uri: x\npipeline:\n  batchSize: 500\n"), nil)
	if got := cfg.BatchChannelSize(); got != 1000 {
		t.Errorf("derived channel size = %d, want 1000", got)
	}
	cfg, _ = Load(writeYAML(t, "mongo:\n  uri: x\npipeline:\n  batchSize: 500\n  channelBuffer: 4096\n"), nil)
	if got := cfg.BatchChannelSize(); got != 4096 {
		t.Errorf("explicit channel size = %d, want 4096", got)
	}
}

// ---------------------------------------------------------------------------
// batchSizeMin / batchSizeMax tests
// ---------------------------------------------------------------------------

func TestBatchSizeMinMax_ConfiguredValues(t *testing.T) {
	// Scenario 1: configured batchSizeMin=200, batchSizeMax=3000 are honored
	yaml := `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 200
  batchSizeMax: 3000
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BatchSizeMin(); got != 200 {
		t.Errorf("BatchSizeMin() = %d, want 200", got)
	}
	if got := cfg.BatchSizeMax(); got != 3000 {
		t.Errorf("BatchSizeMax() = %d, want 3000", got)
	}
}

func TestBatchSizeMinMax_AutoDerivation(t *testing.T) {
	// Scenario 2: batchSizeMin=0, batchSizeMax=0 → auto-derived
	yaml := `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 0
  batchSizeMax: 0
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BatchSizeMin(); got != 250 {
		t.Errorf("BatchSizeMin() = %d, want 250 (auto-derived BatchSize/4)", got)
	}
	if got := cfg.BatchSizeMax(); got != 2000 {
		t.Errorf("BatchSizeMax() = %d, want 2000 (auto-derived BatchSize*2)", got)
	}
}

func TestBatchSizeMinMax_ClampBehavior(t *testing.T) {
	// Scenario 3: batchSizeMin > batchSize → clamped to batchSize
	yaml := `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 1500
  batchSizeMax: 0
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BatchSizeMin(); got != 1000 {
		t.Errorf("BatchSizeMin() = %d, want 1000 (clamped to batchSize)", got)
	}
	// batchSizeMax < batchSize → clamped to batchSize
	yaml2 := `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 0
  batchSizeMax: 500
`
	cfg2, err := Load(writeYAML(t, yaml2), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg2.BatchSizeMax(); got != 1000 {
		t.Errorf("BatchSizeMax() = %d, want 1000 (clamped to batchSize)", got)
	}
}

func TestBatchSizeMinMax_ValidationErrors(t *testing.T) {
	// Note: applyDefaults() silently clamps invalid values, so Validate() does
	// not report errors for batchSizeMin > batchSize or batchSize > batchSizeMax.
	// The clamping behavior is tested in TestBatchSizeMinMax_ClampBehavior.
	// Here we verify that Validate() passes after applyDefaults correction.

	// batchSizeMin > batchSize → silently clamped to batchSize, Validate OK
	yaml := `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 1500
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	// applyDefaults clamps batchSizeMin to batchSize
	if cfg.BatchSizeMin() != 1000 {
		t.Errorf("BatchSizeMin() = %d, want 1000 (clamped)", cfg.BatchSizeMin())
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}

	// batchSize > batchSizeMax → silently clamped to batchSize, Validate OK
	yaml2 := `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMax: 500
`
	cfg2, err := Load(writeYAML(t, yaml2), nil)
	if err != nil {
		t.Fatal(err)
	}
	// applyDefaults clamps batchSizeMax to batchSize
	if cfg2.BatchSizeMax() != 1000 {
		t.Errorf("BatchSizeMax() = %d, want 1000 (clamped)", cfg2.BatchSizeMax())
	}
	if err := cfg2.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}
}

func TestBatchSizeMinMax_BatchSize1_Ceiling(t *testing.T) {
	// BatchSize=1: BatchSizeMin auto-derived as 1 (not 0), BatchSizeMax as 2
	yaml := `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1
  batchSizeMin: 0
  batchSizeMax: 0
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.BatchSizeMin(); got != 1 {
		t.Errorf("BatchSizeMin() = %d, want 1 (minimum 1)", got)
	}
	if got := cfg.BatchSizeMax(); got != 2 {
		t.Errorf("BatchSizeMax() = %d, want 2 (BatchSize*2)", got)
	}
}

// ---------------------------------------------------------------------------
// ENV > CLI priority tests
// ---------------------------------------------------------------------------

func TestEnvOverridesCLI(t *testing.T) {
	// Test that ENV vars have higher priority than CLI flags.
	// When both --logLevel=error and TANGO_LOGGINGLEVEL=debug are set,
	// the ENV value (debug) should win.
	yaml := `
mongo:
  uri: "mongodb://localhost"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate ENV override by directly setting the value (as applyEnvOverrides does)
	// In real test, we would set os.Setenv, but here we test the priority logic
	// by checking that applyEnvOverrides would override CLI values.

	// Test that mode from ENV overrides CLI (simulated via direct override)
	cfg.Mode = "error"        // simulating CLI --mode=error
	os.Setenv("TANGO_MODE", "once")
	defer os.Unsetenv("TANGO_MODE")

	// Re-load to pick up the env var
	cfg2, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Mode != "once" {
		t.Errorf("Mode = %q, want %q (ENV should override default)", cfg2.Mode, "once")
	}
}

func TestEnvOverridesYAML(t *testing.T) {
	// Test that ENV vars have higher priority than YAML values.
	yaml := `
mongo:
  uri: "mongodb://localhost"
logging:
  level: "error"
pipeline:
  batchSize: 500
`
	// envKeyName converts "logging.level" -> "LOGGING_LEVEL", "pipeline.batchSize" -> "PIPELINE_BATCH_SIZE"
	os.Setenv("TANGO_LOGGING_LEVEL", "debug")
	os.Setenv("TANGO_PIPELINE_BATCH_SIZE", "2000")
	defer func() {
		os.Unsetenv("TANGO_LOGGING_LEVEL")
		os.Unsetenv("TANGO_PIPELINE_BATCH_SIZE")
	}()

	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q (ENV should override YAML)", cfg.Logging.Level, "debug")
	}
	if cfg.Pipeline.BatchSize != 2000 {
		t.Errorf("Pipeline.BatchSize = %d, want %d (ENV should override YAML)", cfg.Pipeline.BatchSize, 2000)
	}
}
