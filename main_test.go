package main

// Ultra coverage for the main package: MAIN-1 (resolveConfigPath precedence and
// existence logic) and MAIN-4 (no role subcommands; root takes no positional
// args and dispatches roles purely via role.mode config).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// MAIN-1 (flag set): when flagVal is non-empty, resolveConfigPath returns it
// VERBATIM and never consults the filesystem or candidate list. The path need
// not exist — it is returned as-is.
func TestResolveConfigPath_FlagSetReturnedVerbatim(t *testing.T) {
	const flag = "/some/explicit/path/does-not-exist.yaml"
	got := resolveConfigPath(flag, "tango.yaml", "tango.yml", "tango.json")
	if got != flag {
		t.Errorf("resolveConfigPath(%q, ...) = %q, want the flag value verbatim", flag, got)
	}

	// Even with zero candidates, an explicit flag is returned unchanged.
	if got := resolveConfigPath(flag); got != flag {
		t.Errorf("resolveConfigPath(%q) with no candidates = %q, want %q", flag, got, flag)
	}
}

// MAIN-1 (none exist): with an empty flag and candidate filenames that do not
// exist next to the test binary, resolveConfigPath returns "" so the loader can
// fall back to defaults + env + flags.
func TestResolveConfigPath_NoneExistReturnsEmpty(t *testing.T) {
	// Use clearly non-existent, unique candidate names so we don't accidentally
	// match a real file sitting next to the test binary.
	got := resolveConfigPath("",
		"tango-nonexistent-zzx1.yaml",
		"tango-nonexistent-zzx2.yml",
		"tango-nonexistent-zzx3.json")
	if got != "" {
		t.Errorf("resolveConfigPath with only non-existent candidates = %q, want \"\"", got)
	}

	// Zero candidates with an empty flag also yields "".
	if got := resolveConfigPath(""); got != "" {
		t.Errorf("resolveConfigPath(\"\") with no candidates = %q, want \"\"", got)
	}
}

// MAIN-1 (exists -> first match, in the binary dir): resolveConfigPath scans the
// directory of the running executable (os.Executable's dir). We materialise the
// candidate files THERE and assert that (a) an existing candidate is returned as
// an absolute path inside that dir, and (b) the FIRST existing candidate in the
// argument order wins, not merely any that exists.
func TestResolveConfigPath_ExistingCandidatePrecedence(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	dir := filepath.Dir(exe)

	// Unique names to avoid clobbering anything real; clean up afterwards.
	first := "tango-resolvecfg-first.yaml"
	second := "tango-resolvecfg-second.yml"
	firstPath := filepath.Join(dir, first)
	secondPath := filepath.Join(dir, second)

	// Create the SECOND candidate only; the first does not exist yet, so the
	// second must be selected.
	if err := os.WriteFile(secondPath, []byte("x"), 0o644); err != nil {
		t.Skipf("cannot write probe file in exe dir %q: %v", dir, err)
	}
	defer os.Remove(secondPath)

	if got := resolveConfigPath("", first, second); got != secondPath {
		t.Errorf("resolveConfigPath skipping missing first = %q, want %q", got, secondPath)
	}

	// Now create the FIRST candidate too: precedence is argument order, so the
	// first must now win even though both exist.
	if err := os.WriteFile(firstPath, []byte("x"), 0o644); err != nil {
		t.Skipf("cannot write probe file in exe dir %q: %v", dir, err)
	}
	defer os.Remove(firstPath)

	if got := resolveConfigPath("", first, second); got != firstPath {
		t.Errorf("resolveConfigPath with both present = %q, want first %q (argument-order precedence)", got, firstPath)
	}
}

// MAIN-1 (directory is not a match): a candidate name that resolves to a
// DIRECTORY (not a regular file) must be skipped — resolveConfigPath requires
// !info.IsDir(). With only a directory candidate present, the result is "".
func TestResolveConfigPath_DirectoryCandidateSkipped(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable unavailable: %v", err)
	}
	dir := filepath.Dir(exe)

	name := "tango-resolvecfg-dir.yaml"
	dirPath := filepath.Join(dir, name)
	if err := os.Mkdir(dirPath, 0o755); err != nil {
		t.Skipf("cannot mkdir probe in exe dir %q: %v", dir, err)
	}
	defer os.Remove(dirPath)

	if got := resolveConfigPath("", name); got != "" {
		t.Errorf("resolveConfigPath with a directory-only candidate = %q, want \"\" (directories must be skipped)", got)
	}
}

// MAIN-4: there are NO role subcommands — the runtime role is chosen solely by
// role.mode config. newRoot() must declare cobra.NoArgs and register no child
// commands that could dispatch a role.
func TestNewRoot_NoArgsNoSubcommands(t *testing.T) {
	root := newRoot()

	// Args must reject any positional argument. cobra.NoArgs returns a non-nil
	// error for any args and nil for none.
	if root.Args == nil {
		t.Fatal("newRoot().Args is nil, want cobra.NoArgs")
	}
	if err := root.Args(root, []string{"daemon"}); err == nil {
		t.Error("root.Args(\"daemon\") = nil, want an error (positional role args must be rejected)")
	}
	if err := root.Args(root, nil); err != nil {
		t.Errorf("root.Args(nil) = %v, want nil (no args is allowed)", err)
	}

	// No subcommands should be registered at all. cobra always carries internal
	// help/completion machinery, but Commands() should contain no user-defined
	// role dispatch command. We assert none of daemon/gateway/cli/api exist as a
	// subcommand, and more strongly that there are no non-builtin commands.
	for _, c := range root.Commands() {
		switch c.Name() {
		case "daemon", "gateway", "cli", "api":
			t.Errorf("newRoot() registered a role subcommand %q; roles must be selected by role.mode only", c.Name())
		}
	}

	if n := countNonBuiltinCommands(root); n != 0 {
		t.Errorf("newRoot() has %d non-builtin subcommand(s); want 0 (no role subcommands)", n)
	}

	// The root itself must be runnable (RunE set) so that with no args it
	// dispatches via run() rather than printing help.
	if root.RunE == nil {
		t.Error("newRoot().RunE is nil; root must run the role pipeline directly")
	}

	// Sanity: the --config persistent flag is registered for default discovery.
	if root.PersistentFlags().Lookup("config") == nil {
		t.Error("newRoot() missing the --config persistent flag")
	}
}

// countNonBuiltinCommands counts subcommands that are not cobra's auto-generated
// help/completion commands.
func countNonBuiltinCommands(root *cobra.Command) int {
	n := 0
	for _, c := range root.Commands() {
		switch c.Name() {
		case "help", "completion":
			continue
		}
		n++
	}
	return n
}
