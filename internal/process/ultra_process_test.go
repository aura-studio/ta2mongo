package process

import (
	"strings"
	"testing"

	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process/batch"
)

// TestUltraProcess_ParseMode covers PROC-1: ParseMode accepts the three valid
// upload strategies (single / batch / pipeline) returning the matching Mode and
// no error, and rejects anything else with a descriptive error and an empty
// Mode.
func TestUltraProcess_ParseMode(t *testing.T) {
	valid := []struct {
		in   string
		want Mode
	}{
		{"single", ModeSingle},
		{"batch", ModeBatch},
		{"pipeline", ModePipeline},
	}
	for _, tc := range valid {
		got, err := ParseMode(tc.in)
		if err != nil {
			t.Errorf("ParseMode(%q) returned error %v, want nil", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	invalid := []string{"", "Single", "BATCH", "stream", "async", "single ", "unknown"}
	for _, in := range invalid {
		got, err := ParseMode(in)
		if err == nil {
			t.Errorf("ParseMode(%q) returned nil error, want a rejection error", in)
		}
		if got != "" {
			t.Errorf("ParseMode(%q) = %q, want empty Mode on error", in, got)
		}
		// The error must name the offending value and the allowed set so the
		// operator can fix their config.
		if err != nil {
			msg := err.Error()
			if !strings.Contains(msg, "unknown upload mode") {
				t.Errorf("ParseMode(%q) error = %q, want it to contain %q", in, msg, "unknown upload mode")
			}
			if !strings.Contains(msg, "single|batch|pipeline") {
				t.Errorf("ParseMode(%q) error = %q, want it to list single|batch|pipeline", in, msg)
			}
		}
	}
}

// TestUltraProcess_ConfigModeValue covers PROC-1 at the Config level: ModeValue
// defaults a nil/empty Mode to batch, returns the parsed Mode for valid values,
// and surfaces ParseMode's error for unknown values.
func TestUltraProcess_ConfigModeValue(t *testing.T) {
	// A nil *Config defaults to batch (the documented zero-config default).
	var nilCfg *Config
	if got, err := nilCfg.ModeValue(); err != nil || got != ModeBatch {
		t.Errorf("(*Config)(nil).ModeValue() = (%q, %v), want (%q, nil)", got, err, ModeBatch)
	}

	// An empty Mode also defaults to batch.
	if got, err := (&Config{Mode: ""}).ModeValue(); err != nil || got != ModeBatch {
		t.Errorf("Config{Mode:\"\"}.ModeValue() = (%q, %v), want (%q, nil)", got, err, ModeBatch)
	}

	// Each explicit valid mode round-trips.
	for _, m := range []Mode{ModeSingle, ModeBatch, ModePipeline} {
		got, err := (&Config{Mode: string(m)}).ModeValue()
		if err != nil {
			t.Errorf("Config{Mode:%q}.ModeValue() error = %v, want nil", m, err)
		}
		if got != m {
			t.Errorf("Config{Mode:%q}.ModeValue() = %q, want %q", m, got, m)
		}
	}

	// An unknown mode is rejected with an empty Mode.
	got, err := (&Config{Mode: "bogus"}).ModeValue()
	if err == nil {
		t.Error("Config{Mode:\"bogus\"}.ModeValue() returned nil error, want rejection")
	}
	if got != "" {
		t.Errorf("Config{Mode:\"bogus\"}.ModeValue() = %q, want empty Mode on error", got)
	}
}

// TestUltraProcess_BatchSizeDefaultFallback covers BATCH-4: a non-positive batch
// size falls back to batch.DefaultBatchSize. The fallback constant is exercised
// two ways without MongoDB:
//
//  1. Config.ApplyDefaults rewrites a non-positive BatchSize to
//     batch.DefaultBatchSize (the value New later passes to batch.NewUploader),
//     so the effective size is directly observable.
//  2. batch.NewUploader itself can be constructed with a non-positive size and a
//     nil store (construction touches neither store nor Mongo); it must return a
//     usable, non-nil Uploader rather than breaking.
func TestUltraProcess_BatchSizeDefaultFallback(t *testing.T) {
	// Pin the default the way the fallback contract assumes it: 1000.
	if batch.DefaultBatchSize != 1000 {
		t.Errorf("batch.DefaultBatchSize = %d, want 1000", batch.DefaultBatchSize)
	}

	// ApplyDefaults must replace each non-positive BatchSize with the default.
	for _, in := range []int{0, -1, -1000} {
		c := &Config{BatchSize: in}
		c.ApplyDefaults()
		if c.BatchSize != batch.DefaultBatchSize {
			t.Errorf("ApplyDefaults with BatchSize=%d gave %d, want %d (DefaultBatchSize)",
				in, c.BatchSize, batch.DefaultBatchSize)
		}
	}

	// A positive BatchSize is preserved (the fallback must NOT clobber a real
	// configured value).
	c := &Config{BatchSize: 42}
	c.ApplyDefaults()
	if c.BatchSize != 42 {
		t.Errorf("ApplyDefaults preserved BatchSize incorrectly: got %d, want 42", c.BatchSize)
	}

	// batch.NewUploader with a non-positive size and nil store must still build a
	// usable Uploader (it falls back to DefaultBatchSize internally instead of
	// panicking or returning nil). Construction does not require a Mongo
	// connection.
	for _, in := range []int{0, -5} {
		u := batch.NewUploader(nil, parser.New(nil), in, nil)
		if u == nil {
			t.Fatalf("batch.NewUploader(nil, parser, %d, nil) = nil, want a non-nil Uploader", in)
		}
	}
}
