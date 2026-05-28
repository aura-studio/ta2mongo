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
// RescanInterval tests
// ---------------------------------------------------------------------------

func TestRescanInterval_Default(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
logPattern:
  - "/tmp/.*\\.log"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := 30 * time.Second
	if got := cfg.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_CustomValue(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
rescanInterval: "60s"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := 60 * time.Second
	if got := cfg.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v", got, want)
	}
}

func TestRescanInterval_ZeroFallsBackToDefault(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
rescanInterval: "0s"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := 30 * time.Second
	if got := cfg.RescanInterval; got != want {
		t.Errorf("RescanInterval = %v, want %v (should fallback for zero)", got, want)
	}
}

// ---------------------------------------------------------------------------
// MaxElapsedTime tests
// ---------------------------------------------------------------------------

func TestMaxElapsedTime_Default(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := 10 * time.Second
	if got := cfg.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_CustomValue(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
maxElapsedTime: "30s"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := 30 * time.Second
	if got := cfg.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

func TestMaxElapsedTime_FallbackForZero(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
maxElapsedTime: "0s"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := 10 * time.Second
	if got := cfg.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v (should fallback for zero)", got, want)
	}
}

func TestMaxElapsedTime_LargeValue(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
maxElapsedTime: "5m"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := 5 * time.Minute
	if got := cfg.MaxElapsedTime; got != want {
		t.Errorf("MaxElapsedTime = %v, want %v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Validation edge cases
// ---------------------------------------------------------------------------

func TestValidation_MissingMongoURI(t *testing.T) {
	yaml := `
logPattern:
  - "/tmp/.*\\.log"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for missing mongoURI")
	}
}

func TestValidation_MissingLogPattern_OK(t *testing.T) {
	// logPattern is not required at config level; validated by daemon at runtime.
	yaml := `
mongoURI: "mongodb://localhost"
`
	_, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidation_EmptyLogPattern_OK(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
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
mongoURI: "mongodb://localhost"
filterInclude:
  - '#type == "track"'
  - '#type == "user_set"'
filterExclude:
  - 'debug == true'
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FilterInclude) != 2 {
		t.Errorf("FilterInclude len = %d, want 2", len(cfg.FilterInclude))
	}
	if len(cfg.FilterExclude) != 1 {
		t.Errorf("FilterExclude len = %d, want 1", len(cfg.FilterExclude))
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
mongoURI: "mongodb://localhost"
filterInclude:
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
mongoURI: "mongodb://localhost"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.FilterInclude) != 0 || len(cfg.FilterExclude) != 0 {
		t.Errorf("expected empty filter lists, got inc=%v exc=%v",
			cfg.FilterInclude, cfg.FilterExclude)
	}
}

func TestFlushInterval(t *testing.T) {
	yaml := `
mongoURI: "mongodb://localhost"
flushInterval: "500ms"
`
	cfg, err := Load(writeYAML(t, yaml), nil)
	if err != nil {
		t.Fatal(err)
	}

	want := 500 * time.Millisecond
	if got := cfg.FlushInterval; got != want {
		t.Errorf("FlushInterval = %v, want %v", got, want)
	}
}
