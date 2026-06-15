package filter

import "testing"

// TestCompileToSQL pins the expr-lang → Presto WHERE translation used by the
// backfill SQL pushdown, including the hash-prefix column rewrite, operator
// mapping, the `in` array form, and include/exclude composition.
func TestCompileToSQL(t *testing.T) {
	cases := []struct {
		name    string
		include []string
		exclude []string
		want    string
	}{
		{
			name: "both empty",
			want: "",
		},
		{
			name:    "single include with hash field",
			include: []string{`#type == "track"`},
			want:    `("#type" = 'track')`,
		},
		{
			name:    "in array",
			include: []string{`#event_name in ["login", "pay"]`},
			want:    `("#event_name" IN ('login', 'pay'))`,
		},
		{
			name:    "multiple includes OR'd",
			include: []string{`#type == "track"`, `level > 3`},
			want:    `(("#type" = 'track') OR ("level" > 3))`,
		},
		{
			name:    "exclude becomes NOT",
			include: []string{`#type == "track"`},
			exclude: []string{`debug == true`},
			want:    `("#type" = 'track') AND NOT (("debug" = TRUE))`,
		},
		{
			name:    "exclude only",
			exclude: []string{`level < 0`},
			want:    `NOT (("level" < 0))`,
		},
		{
			name:    "logical and",
			include: []string{`#type == "track" && level >= 5`},
			want:    `(("#type" = 'track' AND "level" >= 5))`,
		},
		{
			name:    "not equal",
			include: []string{`#type != "user_set"`},
			want:    `("#type" <> 'user_set')`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CompileToSQL(c.include, c.exclude)
			if err != nil {
				t.Fatalf("CompileToSQL: %v", err)
			}
			if got != c.want {
				t.Errorf("CompileToSQL\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}

func TestCompileToSQL_Unsupported(t *testing.T) {
	// Function calls are not pushdown-safe and must error.
	if _, err := CompileToSQL([]string{`lower(#type) == "track"`}, nil); err == nil {
		t.Fatal("expected error for unsupported function call")
	}
}
