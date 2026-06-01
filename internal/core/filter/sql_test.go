package filter

import (
	"strings"
	"testing"
)

func TestCompileToSQL_EmptyBoth(t *testing.T) {
	sql, err := CompileToSQL(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sql != "" {
		t.Errorf("empty both: got %q, want \"\"", sql)
	}
}

func TestCompileToSQL_IncludeOnly_Single(t *testing.T) {
	sql, err := CompileToSQL([]string{`#type == "track"`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `("#type" = 'track')`
	if sql != want {
		t.Errorf("got %q, want %q", sql, want)
	}
}

func TestCompileToSQL_IncludeOnly_Multiple_OR(t *testing.T) {
	sql, err := CompileToSQL([]string{
		`#type == "track"`,
		`#type == "user_set"`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `(("#type" = 'track') OR ("#type" = 'user_set'))`
	if sql != want {
		t.Errorf("got %q, want %q", sql, want)
	}
}

func TestCompileToSQL_ExcludeOnly(t *testing.T) {
	sql, err := CompileToSQL(nil, []string{`debug == true`})
	if err != nil {
		t.Fatal(err)
	}
	want := `NOT (("debug" = TRUE))`
	if sql != want {
		t.Errorf("got %q, want %q", sql, want)
	}
}

func TestCompileToSQL_IncludeAndExclude(t *testing.T) {
	sql, err := CompileToSQL(
		[]string{`#type == "track"`},
		[]string{`debug == true`},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := `("#type" = 'track') AND NOT (("debug" = TRUE))`
	if sql != want {
		t.Errorf("got %q, want %q", sql, want)
	}
}

func TestCompileToSQL_Operators(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`level >= 5`, `("level" >= 5)`},
		{`level <= 100`, `("level" <= 100)`},
		{`level < 5`, `("level" < 5)`},
		{`level > 5`, `("level" > 5)`},
		{`level == 5`, `("level" = 5)`},
		{`level != 5`, `("level" <> 5)`},
		{`country in ["CN", "US"]`, `("country" IN ('CN', 'US'))`},
		{`level >= 5 && country == "CN"`, `(("level" >= 5 AND "country" = 'CN'))`},
		{`level >= 5 || country == "CN"`, `(("level" >= 5 OR "country" = 'CN'))`},
		{`!debug`, `(NOT ("debug"))`},
		{`!(debug == true)`, `(NOT ("debug" = TRUE))`},
		{`score == 1.5`, `("score" = 1.5)`},
		{`name == "O'Reilly"`, `("name" = 'O''Reilly')`}, // single-quote escape
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			sql, err := CompileToSQL([]string{tc.in}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if sql != tc.want {
				t.Errorf("got %q, want %q", sql, tc.want)
			}
		})
	}
}

func TestCompileToSQL_HashFields(t *testing.T) {
	sql, err := CompileToSQL([]string{
		`#type == "track" && #event_name in ["login", "pay"]`,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `(("#type" = 'track' AND "#event_name" IN ('login', 'pay')))`
	if sql != want {
		t.Errorf("got %q, want %q", sql, want)
	}
}

func TestCompileToSQL_Unsupported(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // substring expected in error
	}{
		{"startsWith call", `#event_name startsWith "track"`, "binary operator"},
		{"function call", `length(name) > 0`, "unsupported"},
		{"ternary", `a ? b : c`, "unsupported"},
		{"regex matches", `name matches "^abc"`, "binary operator"},
		{"member chain", `user.profile.level > 0`, "$env"},
		{"in with non-literal", `country in countries`, "array literal"},
		{"empty array", `x in []`, "array literal cannot be empty"},
		{"array with var", `country in [country]`, "must be literals"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CompileToSQL([]string{tc.in}, nil)
			if err == nil {
				t.Fatalf("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestCompileToSQL_QuoteEscape(t *testing.T) {
	// Identifier containing a literal " — pathological but should escape.
	// We can only trigger this via $env access in the rewriter, so build manually.
	sql, err := compileOneToSQL(`#"weird" == 1`)
	if err == nil {
		t.Fatalf("expected parse error for malformed hash ident, got %q", sql)
	}
}
