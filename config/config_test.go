package config

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

// loadFlat unmarshals a YAML document straight into Config and applies defaults.
// It exercises applyDefaults / Validate / the pipeline helpers directly,
// independent of the env/flag loading path (covered in loader_test.go). Viper's
// default decoder already handles string durations and comma slices.
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
// source.tailer.rescanInterval
// ---------------------------------------------------------------------------

func TestRescanInterval_Default(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
source:
  tailer:
    logPattern:
      - "/tmp/.*\\.log"
`)
	if got, want := cfg.Source.Tailer.RescanInterval, 30*time.Second; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_CustomValue(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
source:
  tailer:
    rescanInterval: "60s"
`)
	if got, want := cfg.Source.Tailer.RescanInterval, 60*time.Second; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_ZeroFallsBackToDefault(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
source:
  tailer:
    rescanInterval: "0s"
`)
	if got, want := cfg.Source.Tailer.RescanInterval, 30*time.Second; got != want {
		t.Errorf("RescanInterval = %v, want %v (should fallback for zero)", got, want)
	}
}

// ---------------------------------------------------------------------------
// dao.store.maxElapsedTime
// ---------------------------------------------------------------------------

func TestMaxElapsedTime_Default(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if got, want := cfg.Dao.Store.MaxElapsedTime, 10*time.Second; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_CustomValue(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
  store:
    maxElapsedTime: "30s"
`)
	if got, want := cfg.Dao.Store.MaxElapsedTime, 30*time.Second; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_FallbackForZero(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
  store:
    maxElapsedTime: "0s"
`)
	if got, want := cfg.Dao.Store.MaxElapsedTime, 10*time.Second; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v (should fallback for zero)", got, want)
	}
}

// ---------------------------------------------------------------------------
// Validation edge cases
// ---------------------------------------------------------------------------

func TestValidation_MissingMongoURI(t *testing.T) {
	cfg := loadFlat(t, `
source:
  tailer:
    logPattern:
      - "/tmp/.*\\.log"
`)
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing dao.mongo.uri")
	}
}

func TestValidation_MissingLogPattern_OK(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Filter expressions (parser.filter.*)
// ---------------------------------------------------------------------------

func TestFilter_LoadsFromYAML(t *testing.T) {
	cfg := loadFlat(t, `
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
	if len(cfg.Parser.Filter.Include) != 2 {
		t.Errorf("Filter.Include len = %d, want 2", len(cfg.Parser.Filter.Include))
	}
	if len(cfg.Parser.Filter.Exclude) != 1 {
		t.Errorf("Filter.Exclude len = %d, want 1", len(cfg.Parser.Filter.Exclude))
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestFilter_ValidationFailsForBadExpression(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
parser:
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
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if len(cfg.Parser.Filter.Include) != 0 || len(cfg.Parser.Filter.Exclude) != 0 {
		t.Errorf("expected empty filter lists, got inc=%v exc=%v",
			cfg.Parser.Filter.Include, cfg.Parser.Filter.Exclude)
	}
}

// ---------------------------------------------------------------------------
// process.* knobs
// ---------------------------------------------------------------------------

func TestFlushInterval(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
process:
  pipeline:
    flushInterval: "500ms"
`)
	if got, want := cfg.Process.Pipeline.FlushInterval, 500*time.Millisecond; got != want {
		t.Errorf("FlushInterval = %v, want %v", got, want)
	}
}

func TestNestedKnobs_Defaults(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if cfg.Dao.Mongo.ConnectTimeout != 10*time.Second {
		t.Errorf("connectTimeout = %v", cfg.Dao.Mongo.ConnectTimeout)
	}
	if cfg.Source.Tailer.PollInterval != 200*time.Millisecond {
		t.Errorf("pollInterval = %v", cfg.Source.Tailer.PollInterval)
	}
	if cfg.Source.Tailer.MaxLineBytes != 10*1024*1024 {
		t.Errorf("maxLineBytes = %d", cfg.Source.Tailer.MaxLineBytes)
	}
	if cfg.Process.Pipeline.DeadLetterCap != 128 {
		t.Errorf("deadLetterCap = %d", cfg.Process.Pipeline.DeadLetterCap)
	}
	if cfg.Logging.Level != "info" || cfg.Logging.Format != "text" {
		t.Errorf("logging = %+v", cfg.Logging)
	}
}

func TestChannelBuffer_DerivedVsExplicit(t *testing.T) {
	cfg := loadFlat(t, "dao:\n  mongo:\n    uri: x\nprocess:\n  pipeline:\n    batchSize: 500\n")
	if got := cfg.Process.Pipeline.ChannelSize(); got != 1000 {
		t.Errorf("derived channel size = %d, want 1000", got)
	}
	cfg = loadFlat(t, "dao:\n  mongo:\n    uri: x\nprocess:\n  pipeline:\n    batchSize: 500\n    channelBuffer: 4096\n")
	if got := cfg.Process.Pipeline.ChannelSize(); got != 4096 {
		t.Errorf("explicit channel size = %d, want 4096", got)
	}
}

func TestBatchSizeMinMax_AutoDerivation(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
process:
  pipeline:
    batchSize: 1000
`)
	if got := cfg.Process.Pipeline.MinBatchSize(); got != 250 {
		t.Errorf("MinBatchSize() = %d, want 250", got)
	}
	if got := cfg.Process.Pipeline.MaxBatchSize(); got != 2000 {
		t.Errorf("MaxBatchSize() = %d, want 2000", got)
	}
}

// ---------------------------------------------------------------------------
// role.gateway.*
// ---------------------------------------------------------------------------

func TestGatewayDefaults(t *testing.T) {
	cfg := loadFlat(t, `
dao:
  mongo:
    uri: "mongodb://localhost"
`)
	if cfg.Role.Gateway.Addr != ":8080" {
		t.Errorf("role.gateway.addr default = %q, want :8080", cfg.Role.Gateway.Addr)
	}
	if cfg.Process.Mode != "batch" {
		t.Errorf("process.mode default = %q, want batch", cfg.Process.Mode)
	}
}

func TestGatewayLoadsFromYAML(t *testing.T) {
	// role.gateway holds only gateway-specific knobs (addr); the
	// upload processing/filter come from the shared top-level process.* /
	// parser.filter.* modules.
	cfg := loadFlat(t, `
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
	if cfg.Role.Gateway.Addr != ":9090" {
		t.Errorf("addr = %q", cfg.Role.Gateway.Addr)
	}
	if cfg.Process.Mode != "pipeline" {
		t.Errorf("process.mode = %q", cfg.Process.Mode)
	}
	if cfg.Process.BatchSize != 250 {
		t.Errorf("process.batchSize = %d", cfg.Process.BatchSize)
	}
	if cfg.Process.Pipeline.BatchWorkers != 4 {
		t.Errorf("process.pipeline.batchWorkers = %d", cfg.Process.Pipeline.BatchWorkers)
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

	yaml := `
logging:
  level: "error"
dao:
  mongo:
    uri: "mongodb://localhost"
source:
  tailer:
    logPattern: ["/tmp/x.log"]
    rescanInterval: "30s"
`
	c, err := Load(writeFile(t, "tango.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Logging.Level != "debug" {
		t.Errorf("logging.level = %q, want debug (ENV should override YAML)", c.Logging.Level)
	}
	if c.Source.Tailer.RescanInterval != 60*time.Second {
		t.Errorf("rescanInterval = %v, want 60s (ENV should override YAML)", c.Source.Tailer.RescanInterval)
	}
}
