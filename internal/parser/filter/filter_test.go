package filter

import (
	"strings"
	"testing"
)

func TestNew_CompileError(t *testing.T) {
	cases := []struct {
		name    string
		include []string
		exclude []string
		want    string
	}{
		{"include syntax error", []string{"#type ==="}, nil, "include[0]"},
		{"exclude syntax error", nil, []string{"&&"}, "exclude[0]"},
		{"non-bool include", []string{`"hello"`}, nil, "include[0]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.include, tc.exclude)
			if err == nil {
				t.Fatalf("expected compile error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestKeep_EmptyFilter(t *testing.T) {
	f, err := New(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !f.Empty() {
		t.Fatalf("Empty() = false on empty filter")
	}
	keep, ferr := f.Keep(map[string]any{"#type": "track"})
	if ferr != nil || !keep {
		t.Fatalf("Keep on empty filter = (%v,%v), want (true,nil)", keep, ferr)
	}
}

func TestKeep_NilReceiver(t *testing.T) {
	var f *Filter
	if !f.Empty() {
		t.Errorf("nil Filter Empty() = false")
	}
	keep, err := f.Keep(map[string]any{"x": 1})
	if !keep || err != nil {
		t.Errorf("nil Filter Keep = (%v,%v), want (true,nil)", keep, err)
	}
}

func TestKeep_IncludeOR(t *testing.T) {
	f, err := New(
		[]string{
			`#type == "track"`,
			`#type == "user_set"`,
		},
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		typ  string
		want bool
	}{
		{"track", true},
		{"user_set", true},
		{"track_signup", false}, // no exact match for any include
	}
	for _, tc := range cases {
		got, _ := f.Keep(map[string]any{"#type": tc.typ})
		if got != tc.want {
			t.Errorf("#type=%q: keep=%v, want %v", tc.typ, got, tc.want)
		}
	}
}

func TestKeep_ExcludeOR(t *testing.T) {
	f, err := New(
		nil,
		[]string{
			`country == "CN"`,
			`debug == true`,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		env  map[string]any
		want bool
	}{
		{map[string]any{"country": "US", "debug": false}, true},
		{map[string]any{"country": "CN", "debug": false}, false},
		{map[string]any{"country": "US", "debug": true}, false},
		{map[string]any{}, true}, // no field present → expressions evaluate to false
	}
	for i, tc := range cases {
		got, _ := f.Keep(tc.env)
		if got != tc.want {
			t.Errorf("case %d env=%v: keep=%v, want %v", i, tc.env, got, tc.want)
		}
	}
}

func TestKeep_IncludeThenExclude(t *testing.T) {
	// Pass include (event must be track*) AND not match exclude (debug==true).
	f, err := New(
		[]string{`#type startsWith "track"`},
		[]string{`debug == true`},
	)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		env  map[string]any
		want bool
	}{
		{"include miss", map[string]any{"#type": "user_set"}, false},
		{"include hit, exclude miss", map[string]any{"#type": "track", "debug": false}, true},
		{"include hit, exclude hit", map[string]any{"#type": "track", "debug": true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := f.Keep(tc.env)
			if got != tc.want {
				t.Errorf("keep=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestKeep_PropertiesFlattened(t *testing.T) {
	// Worker passes the flattened Record.Doc, where "properties.*" keys are
	// promoted to top-level. The filter sees them directly.
	f, err := New([]string{`level >= 5 && country in ["CN", "US"]`}, nil)
	if err != nil {
		t.Fatal(err)
	}

	keep, ferr := f.Keep(map[string]any{
		"#type":   "track",
		"level":   7,
		"country": "CN",
	})
	if ferr != nil {
		t.Fatalf("eval error: %v", ferr)
	}
	if !keep {
		t.Errorf("expected keep=true for matching properties")
	}
}

func TestRewriteHashRefs(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{`#type == "track"`, `$env["#type"] == "track"`},
		{`#type == "#track"`, `$env["#type"] == "#track"`}, // hash in string is preserved
		{`a == b`, `a == b`},                               // no hashes
		{`#event_name in ["a", "#b"]`, `$env["#event_name"] in ["a", "#b"]`},
		{`tag == '#x' && #type == "user_set"`, `tag == '#x' && $env["#type"] == "user_set"`},
		{"`#raw` == \"x\"", "`#raw` == \"x\""}, // backtick literal preserved as-is
		{`# notIdent`, `# notIdent`},           // lone # not followed by identifier start
	}
	for _, tc := range cases {
		got := rewriteHashRefs(tc.in)
		if got != tc.want {
			t.Errorf("rewriteHashRefs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestKeep_IncludeEvalErrorFailsOpen(t *testing.T) {
	// Force a runtime type error: comparing an int with a string. The expression
	// compiles fine; it only errors on this record's data.
	f, err := New([]string{`level > "abc"`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keep, ferr := f.Keep(map[string]any{"level": 10})
	if ferr == nil {
		t.Fatalf("expected eval error to be reported")
	}
	// The sole include rule errored, so we cannot prove the record is excluded.
	// Fail open: keep it (and the error is reported for OnFilterError). The
	// pre-fix behaviour dropped it here — silent loss of a valid record (B6).
	if !keep {
		t.Errorf("expected keep=true (fail open) on include eval error, got false")
	}
}

func TestKeep_ExcludeEvalErrorFailsOpen(t *testing.T) {
	// An exclude rule that errors on the record must NOT exclude it (fail open).
	f, err := New(nil, []string{`level > "abc"`})
	if err != nil {
		t.Fatal(err)
	}
	keep, ferr := f.Keep(map[string]any{"level": 10})
	if ferr == nil {
		t.Fatalf("expected eval error to be reported")
	}
	if !keep {
		t.Errorf("expected keep=true (fail open) on exclude eval error, got false")
	}
}

func TestKeep_DefinitiveExcludeStillDropsDespiteOtherError(t *testing.T) {
	// Fail-open must stay precise: when one exclude rule errors but ANOTHER
	// exclude definitively matches, the record is still dropped — the error must
	// not resurrect a record that genuinely matched an exclude.
	f, err := New(nil, []string{`level > "abc"`, `country == "CN"`})
	if err != nil {
		t.Fatal(err)
	}
	keep, ferr := f.Keep(map[string]any{"level": 10, "country": "CN"})
	if ferr == nil {
		t.Fatalf("expected the first exclude rule's eval error to be reported")
	}
	if keep {
		t.Errorf("expected keep=false: the second exclude rule definitively matched")
	}
}

func TestKeep_IncludeErrorButAnotherMatches(t *testing.T) {
	// An errored include rule must not break a real match by a sibling rule.
	f, err := New([]string{`level > "abc"`, `#type == "track"`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	keep, _ := f.Keep(map[string]any{"level": 10, "#type": "track"})
	if !keep {
		t.Errorf("expected keep=true: a sibling include rule matched")
	}
}
