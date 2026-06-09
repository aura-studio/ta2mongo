package daemon

// Ultra coverage for DMN-13 / X-6: maskURI masks the credentials portion of a
// MongoDB URI for safe logging. These are white-box (package daemon) tests of
// the unexported maskURI helper in role.go. Assertions are on the EXACT string
// returned by the real function — the production logic is:
//   - find "://"; if absent, return the input unchanged
//   - find '@' after "://"; if absent, return the input unchanged
//   - otherwise replace everything between "://" and '@' with "***:***"

import "testing"

// TestUltraMaskURI_Credentials: a full user:pass@host URI has its credentials
// replaced verbatim with "***:***", host/port/db preserved exactly.
func TestUltraMaskURI_Credentials(t *testing.T) {
	const in = "mongodb://user:pass@host:27017/db"
	const want = "mongodb://***:***@host:27017/db"
	if got := maskURI(in); got != want {
		t.Errorf("maskURI(%q) = %q, want %q", in, got, want)
	}
}

// TestUltraMaskURI_Table covers the behavioral branches of maskURI with exact
// expected outputs derived from the real implementation.
func TestUltraMaskURI_Table(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// Canonical credentialed URI.
			name: "user_pass",
			in:   "mongodb://user:pass@host:27017/db",
			want: "mongodb://***:***@host:27017/db",
		},
		{
			// SRV scheme is just another "://" prefix; creds still masked,
			// everything after '@' preserved including query string.
			name: "srv_with_query",
			in:   "mongodb+srv://admin:s3cr3t@cluster0.example.net/test?retryWrites=true",
			want: "mongodb+srv://***:***@cluster0.example.net/test?retryWrites=true",
		},
		{
			// Username only, no password — still collapsed to "***:***".
			name: "user_only",
			in:   "mongodb://onlyuser@host:27017",
			want: "mongodb://***:***@host:27017",
		},
		{
			// No '@' => no credentials => passthrough unchanged.
			name: "no_at",
			in:   "mongodb://host:27017/db",
			want: "mongodb://host:27017/db",
		},
		{
			// No "://" scheme separator => passthrough unchanged, even though
			// it contains an '@' (the scheme check happens first).
			name: "no_scheme_with_at",
			in:   "user:pass@host:27017",
			want: "user:pass@host:27017",
		},
		{
			// Plain non-URI string with neither "://" nor '@' => passthrough.
			name: "plain_string",
			in:   "not-a-uri",
			want: "not-a-uri",
		},
		{
			// Empty string => passthrough (no "://").
			name: "empty",
			in:   "",
			want: "",
		},
		{
			// The '@' must come AFTER "://". Here the only '@' is before the
			// scheme separator, so rest ("host/db") has no '@' => passthrough.
			name: "at_before_scheme",
			in:   "weird@scheme://host/db",
			want: "weird@scheme://host/db",
		},
		{
			// Multiple '@': masking stops at the FIRST '@' after "://"
			// (strings.IndexByte finds the first). Anything after it, including
			// a second '@', is preserved verbatim.
			name: "multiple_at",
			in:   "mongodb://u:p@host/db@weird",
			want: "mongodb://***:***@host/db@weird",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := maskURI(c.in); got != c.want {
				t.Errorf("maskURI(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestUltraMaskURI_NeverLeaksPassword guards the security property: for a
// credentialed URI the literal password substring must NOT appear in the output.
func TestUltraMaskURI_NeverLeaksPassword(t *testing.T) {
	const in = "mongodb://admin:SuperSecret123@db.internal:27017/prod"
	got := maskURI(in)
	if got == in {
		t.Fatalf("maskURI did not mask a credentialed URI: %q", got)
	}
	for _, secret := range []string{"admin", "SuperSecret123"} {
		if contains(got, secret) {
			t.Errorf("maskURI(%q) leaked %q in output %q", in, secret, got)
		}
	}
	if !contains(got, "***:***@db.internal:27017/prod") {
		t.Errorf("maskURI(%q) = %q, expected masked creds + preserved host/db", in, got)
	}
}

// contains is a tiny helper to avoid importing strings just for a substring
// check (and to make the leak assertions self-documenting).
func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
