package logging

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/sirupsen/logrus"
)

// saveLoggerState captures the current global logger state and returns a
// restore func. The package-level helpers all delegate to the single process-
// wide std logger, so any test that calls Init / SetFormatter / SetOutput must
// restore it afterwards to avoid leaking state into sibling tests.
func saveLoggerState(t *testing.T) {
	t.Helper()
	lvl := std.GetLevel()
	fmtr := std.Formatter
	out := std.Out
	t.Cleanup(func() {
		std.SetLevel(lvl)
		std.SetFormatter(fmtr)
		std.SetOutput(out)
	})
}

// ---------------------------------------------------------------------------
// LOG-1: Init applies level + format; nil/unknown falls back to info/text and
// never panics.
// ---------------------------------------------------------------------------

func TestUltraInitAppliesLevelAndFormat(t *testing.T) {
	saveLoggerState(t)

	cases := []struct {
		name      string
		level     string
		format    string
		wantLevel logrus.Level
		wantJSON  bool // true => JSONFormatter, false => TextFormatter
	}{
		{"debug-text", "debug", "text", logrus.DebugLevel, false},
		{"info-text", "info", "text", logrus.InfoLevel, false},
		{"warn-json", "warn", "json", logrus.WarnLevel, true},
		{"error-json", "error", "json", logrus.ErrorLevel, true},
		// Case-insensitivity: Init lower-cases before parsing.
		{"upper-DEBUG-JSON", "DEBUG", "JSON", logrus.DebugLevel, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			Init(&Config{Level: tc.level, Format: tc.format})

			if got := L().GetLevel(); got != tc.wantLevel {
				t.Fatalf("level: got %v, want %v", got, tc.wantLevel)
			}

			switch L().Formatter.(type) {
			case *logrus.JSONFormatter:
				if !tc.wantJSON {
					t.Fatalf("formatter: got JSONFormatter, want TextFormatter")
				}
			case *logrus.TextFormatter:
				if tc.wantJSON {
					t.Fatalf("formatter: got TextFormatter, want JSONFormatter")
				}
			default:
				t.Fatalf("formatter: unexpected type %T", L().Formatter)
			}
		})
	}
}

func TestUltraInitNilConfigFallsBack(t *testing.T) {
	saveLoggerState(t)

	// Put the logger into a non-default state first, so a successful fallback is
	// observable (it must move BACK to info/text).
	Init(&Config{Level: "error", Format: "json"})

	Init(nil) // must not panic

	if got := L().GetLevel(); got != logrus.InfoLevel {
		t.Fatalf("nil cfg level: got %v, want InfoLevel", got)
	}
	if _, ok := L().Formatter.(*logrus.TextFormatter); !ok {
		t.Fatalf("nil cfg formatter: got %T, want *logrus.TextFormatter", L().Formatter)
	}
}

func TestUltraInitUnknownValuesFallBack(t *testing.T) {
	saveLoggerState(t)

	// Start from a non-default state so the fallback is observable.
	Init(&Config{Level: "debug", Format: "json"})

	Init(&Config{Level: "louder", Format: "yaml"}) // unrecognised -> defaults

	if got := L().GetLevel(); got != logrus.InfoLevel {
		t.Fatalf("unknown level: got %v, want InfoLevel", got)
	}
	if _, ok := L().Formatter.(*logrus.TextFormatter); !ok {
		t.Fatalf("unknown format: got %T, want *logrus.TextFormatter", L().Formatter)
	}
}

func TestUltraInitEmptyConfigFallsBack(t *testing.T) {
	saveLoggerState(t)

	Init(&Config{Level: "error", Format: "json"})

	Init(&Config{}) // empty strings -> defaults info/text

	if got := L().GetLevel(); got != logrus.InfoLevel {
		t.Fatalf("empty level: got %v, want InfoLevel", got)
	}
	if _, ok := L().Formatter.(*logrus.TextFormatter); !ok {
		t.Fatalf("empty format: got %T, want *logrus.TextFormatter", L().Formatter)
	}
}

// ---------------------------------------------------------------------------
// LOG-2: Config.Validate() accepts "" and valid values, rejects invalid level
// and invalid format with a descriptive error.
// ---------------------------------------------------------------------------

func TestUltraValidateAcceptsValidAndEmpty(t *testing.T) {
	valid := []Config{
		{Level: "", Format: ""},
		{Level: "debug", Format: "text"},
		{Level: "info", Format: "json"},
		{Level: "warn", Format: ""},
		{Level: "error", Format: "text"},
		{Level: "", Format: "json"},
	}
	for _, c := range valid {
		c := c
		if err := c.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", c, err)
		}
	}
}

func TestUltraValidateRejectsInvalidLevel(t *testing.T) {
	c := Config{Level: "trace", Format: "json"}
	err := c.Validate()
	if err == nil {
		t.Fatalf("Validate with invalid level returned nil error")
	}
	want := `level must be debug/info/warn/error, got "trace"`
	if err.Error() != want {
		t.Fatalf("error string: got %q, want %q", err.Error(), want)
	}
}

func TestUltraValidateRejectsInvalidFormat(t *testing.T) {
	// Level is valid here so we isolate the format branch.
	c := Config{Level: "info", Format: "xml"}
	err := c.Validate()
	if err == nil {
		t.Fatalf("Validate with invalid format returned nil error")
	}
	want := `format must be text/json, got "xml"`
	if err.Error() != want {
		t.Fatalf("error string: got %q, want %q", err.Error(), want)
	}
}

func TestUltraValidateLevelCheckedBeforeFormat(t *testing.T) {
	// Both invalid: the level branch runs first, so the error is the level one.
	c := Config{Level: "bogus", Format: "bogus"}
	err := c.Validate()
	if err == nil {
		t.Fatalf("Validate with both invalid returned nil error")
	}
	want := `level must be debug/info/warn/error, got "bogus"`
	if err.Error() != want {
		t.Fatalf("error string: got %q, want %q", err.Error(), want)
	}
}

// ---------------------------------------------------------------------------
// LOG-4: package helpers are usable BEFORE Init (std logger non-nil); calling
// them does not panic and L() is non-nil.
// ---------------------------------------------------------------------------

func TestUltraHelpersUsableBeforeInit(t *testing.T) {
	saveLoggerState(t)

	// L() must be non-nil at package init time, before any Init call.
	if L() == nil {
		t.Fatal("L() is nil before Init")
	}

	// Discard output so the helpers don't spew during the test, but still
	// exercise the full logging path. Set debug level so Debug* are emitted too.
	L().SetOutput(io.Discard)
	L().SetLevel(logrus.DebugLevel)

	// None of these may panic when called before Init configures anything.
	WithField("k", "v").Info("with field")
	WithFields(Fields{"a": 1, "b": 2}).Warn("with fields")
	WithError(io.EOF).Error("with error")
	Info("info")
	Infof("infof %d", 1)
	Warn("warn")
	Warnf("warnf %d", 2)
	Error("error")
	Errorf("errorf %d", 3)
	Debug("debug")
	Debugf("debugf %d", 4)
}

// ---------------------------------------------------------------------------
// LOG-5: format=json produces valid JSON carrying the structured fields.
// ---------------------------------------------------------------------------

func TestUltraJSONFormatEmitsStructuredFields(t *testing.T) {
	saveLoggerState(t)

	Init(&Config{Level: "info", Format: "json"})

	var buf bytes.Buffer
	L().SetOutput(&buf)

	WithField("request_id", "abc-123").
		WithField("attempt", 7).
		Info("processing request")

	// The buffer may contain a trailing newline; json.Unmarshal handles the
	// single object fine, but be explicit about taking the first line.
	raw := bytes.TrimSpace(buf.Bytes())
	if len(raw) == 0 {
		t.Fatal("no log output captured")
	}

	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("log output is not valid JSON: %v\noutput: %s", err, raw)
	}

	if got, ok := entry["request_id"]; !ok || got != "abc-123" {
		t.Fatalf("request_id field: got %v (present=%v), want %q", got, ok, "abc-123")
	}
	// JSON numbers decode to float64.
	if got, ok := entry["attempt"]; !ok || got != float64(7) {
		t.Fatalf("attempt field: got %v (present=%v), want 7", got, ok)
	}
	if got, ok := entry["msg"]; !ok || got != "processing request" {
		t.Fatalf("msg field: got %v (present=%v), want %q", got, ok, "processing request")
	}
	if got, ok := entry["level"]; !ok || got != "info" {
		t.Fatalf("level field: got %v (present=%v), want %q", got, ok, "info")
	}
}

// Ensure the json package import is genuinely exercised even if the test above
// is filtered out by a -run regex; also a tiny guard that the raw type assert
// compiles.
var _ io.Writer = (*bytes.Buffer)(nil)
