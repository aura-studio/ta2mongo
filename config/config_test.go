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
