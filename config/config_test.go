package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// loadFlat unmarshals a flat YAML document straight into the runtime Config and
// applies defaults. It is a test vehicle for exercising applyDefaults /
// Validate / the BatchSize helpers on the runtime Config directly, independent
// of the role loaders (which are covered in loader_test.go).
func loadFlat(t *testing.T, y string) Config {
	t.Helper()
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.ReadConfig(strings.NewReader(y)); err != nil {
		t.Fatal(err)
	}
	var c Config
	if err := v.Unmarshal(&c); err != nil {
		t.Fatal(err)
	}
	applyDefaults(&c)
	return c
}

// ---------------------------------------------------------------------------
// source.rescanInterval tests
// ---------------------------------------------------------------------------

func TestRescanInterval_Default(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
source:
  logPattern:
    - "/tmp/.*\\.log"
`)
	want := 30 * time.Second
	if got := cfg.Source.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_CustomValue(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
source:
  rescanInterval: "60s"
`)
	want := 60 * time.Second
	if got := cfg.Source.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_ZeroFallsBackToDefault(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
source:
  rescanInterval: "0s"
`)
	want := 30 * time.Second
	if got := cfg.Source.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v (should fallback for zero)", got, want)
	}
}

// ---------------------------------------------------------------------------
// mongo.maxElapsedTime tests
// ---------------------------------------------------------------------------

func TestMaxElapsedTime_Default(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
`)
	want := 10 * time.Second
	if got := cfg.Store.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_CustomValue(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
store:
  maxElapsedTime: "30s"
`)
	want := 30 * time.Second
	if got := cfg.Store.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_FallbackForZero(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
store:
  maxElapsedTime: "0s"
`)
	want := 10 * time.Second
	if got := cfg.Store.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v (should fallback for zero)", got, want)
	}
}

func TestMaxElapsedTime_LargeValue(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
store:
  maxElapsedTime: "5m"
`)
	want := 5 * time.Minute
	if got := cfg.Store.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Validation edge cases
// ---------------------------------------------------------------------------

func TestValidation_MissingMongoURI(t *testing.T) {
	cfg := loadFlat(t, `
source:
  logPattern:
    - "/tmp/.*\\.log"
`)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing mongo.uri")
	}
}

func TestValidation_MissingLogPattern_OK(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidation_EmptyLogPattern_OK(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
source:
  logPattern: []
`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Filter expression tests
// ---------------------------------------------------------------------------

func TestFilter_LoadsFromYAML(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
filter:
  include:
    - '#type == "track"'
    - '#type == "user_set"'
  exclude:
    - 'debug == true'
`)
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
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
filter:
  include:
    - '#type =='
`)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Validate to fail for malformed expression")
	}
}

func TestFilter_EmptyByDefault(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
`)
	if len(cfg.Filter.Include) != 0 || len(cfg.Filter.Exclude) != 0 {
		t.Errorf("expected empty filter lists, got inc=%v exc=%v",
			cfg.Filter.Include, cfg.Filter.Exclude)
	}
}

func TestFlushInterval(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
pipeline:
  flushInterval: "500ms"
`)
	want := 500 * time.Millisecond
	if got := cfg.Pipeline.FlushInterval; got != want {
		t.Errorf("FlushInterval = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Nested-knob smoke tests
// ---------------------------------------------------------------------------

func TestNestedKnobs_Defaults(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
`)
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
	cfg := loadFlat(t, "mongo:\n  uri: x\npipeline:\n  batchSize: 500\n")
	if got := cfg.Pipeline.ChannelSize(); got != 1000 {
		t.Errorf("derived channel size = %d, want 1000", got)
	}
	cfg = loadFlat(t, "mongo:\n  uri: x\npipeline:\n  batchSize: 500\n  channelBuffer: 4096\n")
	if got := cfg.Pipeline.ChannelSize(); got != 4096 {
		t.Errorf("explicit channel size = %d, want 4096", got)
	}
}

// ---------------------------------------------------------------------------
// batchSizeMin / batchSizeMax tests
// ---------------------------------------------------------------------------

func TestBatchSizeMinMax_ConfiguredValues(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 200
  batchSizeMax: 3000
`)
	if got := cfg.Pipeline.MinBatchSize(); got != 200 {
		t.Errorf("BatchSizeMin() = %d, want 200", got)
	}
	if got := cfg.Pipeline.MaxBatchSize(); got != 3000 {
		t.Errorf("BatchSizeMax() = %d, want 3000", got)
	}
}

func TestBatchSizeMinMax_AutoDerivation(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 0
  batchSizeMax: 0
`)
	if got := cfg.Pipeline.MinBatchSize(); got != 250 {
		t.Errorf("BatchSizeMin() = %d, want 250 (auto-derived BatchSize/4)", got)
	}
	if got := cfg.Pipeline.MaxBatchSize(); got != 2000 {
		t.Errorf("BatchSizeMax() = %d, want 2000 (auto-derived BatchSize*2)", got)
	}
}

func TestBatchSizeMinMax_ClampBehavior(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 1500
  batchSizeMax: 0
`)
	if got := cfg.Pipeline.MinBatchSize(); got != 1000 {
		t.Errorf("BatchSizeMin() = %d, want 1000 (clamped to batchSize)", got)
	}
	cfg2 := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 0
  batchSizeMax: 500
`)
	if got := cfg2.Pipeline.MaxBatchSize(); got != 1000 {
		t.Errorf("BatchSizeMax() = %d, want 1000 (clamped to batchSize)", got)
	}
}

func TestBatchSizeMinMax_ValidationErrors(t *testing.T) {
	// applyDefaults silently clamps invalid values, so Validate passes.
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMin: 1500
`)
	if cfg.Pipeline.MinBatchSize() != 1000 {
		t.Errorf("BatchSizeMin() = %d, want 1000 (clamped)", cfg.Pipeline.MinBatchSize())
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}

	cfg2 := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1000
  batchSizeMax: 500
`)
	if cfg2.Pipeline.MaxBatchSize() != 1000 {
		t.Errorf("BatchSizeMax() = %d, want 1000 (clamped)", cfg2.Pipeline.MaxBatchSize())
	}
	if err := cfg2.Validate(); err != nil {
		t.Errorf("Validate() unexpected error: %v", err)
	}
}

func TestBatchSizeMinMax_BatchSize1_Ceiling(t *testing.T) {
	cfg := loadFlat(t, `
mongo:
  uri: "mongodb://localhost"
pipeline:
  batchSize: 1
  batchSizeMin: 0
  batchSizeMax: 0
`)
	if got := cfg.Pipeline.MinBatchSize(); got != 1 {
		t.Errorf("BatchSizeMin() = %d, want 1 (minimum 1)", got)
	}
	if got := cfg.Pipeline.MaxBatchSize(); got != 2 {
		t.Errorf("BatchSizeMax() = %d, want 2 (BatchSize*2)", got)
	}
}

// ---------------------------------------------------------------------------
// ENV override (role loader path): TANGO_* env overrides the YAML file.
// ---------------------------------------------------------------------------

func TestEnvOverridesYAML(t *testing.T) {
	os.Setenv("TANGO_RUNTIME_LOGGING_LEVEL", "debug")
	os.Setenv("TANGO_REPORT_SOURCE_RESCANINTERVAL", "60s")
	defer func() {
		os.Unsetenv("TANGO_RUNTIME_LOGGING_LEVEL")
		os.Unsetenv("TANGO_REPORT_SOURCE_RESCANINTERVAL")
	}()

	yaml := `
runtime:
  mongo:
    uri: "mongodb://localhost"
  logging:
    level: "error"
report:
  source:
    logPattern: ["/tmp/x.log"]
    rescanInterval: "30s"
`
	_, rt, err := LoadStandalone(writeFile(t, "standalone.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	// ENV (string) overrides YAML.
	if rt.Logging.Level != "debug" {
		t.Errorf("Logging.Level = %q, want %q (ENV should override YAML)", rt.Logging.Level, "debug")
	}
	// ENV (duration, via decode hook) overrides YAML.
	if rt.Source.RescanInterval != 60*time.Second {
		t.Errorf("RescanInterval = %v, want 60s (ENV should override YAML)", rt.Source.RescanInterval)
	}
}
