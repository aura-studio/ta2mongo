package file

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// drain collects every line the source emits until its channel closes.
func drain(t *testing.T, src *Source) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var got []string
	out := src.Run(ctx)
	for {
		select {
		case line, ok := <-out:
			if !ok {
				return got
			}
			got = append(got, line)
		case <-ctx.Done():
			t.Fatalf("source did not close in time; got %d lines", len(got))
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestImportsExplicitFilesInOrder: the listed files are read start-to-EOF in the
// given order, blank lines skipped, then the channel closes.
func TestImportsExplicitFilesInOrder(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.log")
	b := filepath.Join(dir, "b.log")
	writeFile(t, a, "a1\n\na2\n")
	writeFile(t, b, "b1\n")

	got := drain(t, New([]string{a, b}, 0))
	want := []string{"a1", "a2", "b1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q (got %v)", i, got[i], want[i], got)
		}
	}
}

// TestDirectoryPathIsSkipped: a directory path is NOT expanded — it is skipped,
// and the other listed files still import.
func TestDirectoryPathIsSkipped(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file INSIDE the directory must not be discovered (no walking).
	writeFile(t, filepath.Join(sub, "inside.log"), "should-not-appear\n")
	f := filepath.Join(dir, "f.log")
	writeFile(t, f, "kept\n")

	got := drain(t, New([]string{sub, f}, 0))
	if len(got) != 1 || got[0] != "kept" {
		t.Fatalf("got %v, want [kept] (directory must be skipped, not expanded)", got)
	}
}

// TestGlobPatternIsLiteralNotExpanded: a glob string is treated as a literal
// path (no expansion), so it matches no real file and is skipped.
func TestGlobPatternIsLiteralNotExpanded(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.log"), "data\n")

	got := drain(t, New([]string{filepath.Join(dir, "*.log")}, 0))
	if len(got) != 0 {
		t.Fatalf("glob pattern must not be expanded; got %v", got)
	}
}

// TestMissingPathIsSkipped: a non-existent path is skipped, the rest import.
func TestMissingPathIsSkipped(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "real.log")
	writeFile(t, f, "ok\n")

	got := drain(t, New([]string{filepath.Join(dir, "nope.log"), f}, 0))
	if len(got) != 1 || got[0] != "ok" {
		t.Fatalf("got %v, want [ok]", got)
	}
}

// TestNoPathsClosesImmediately: an empty path list yields an empty, closed
// source (no panic).
func TestNoPathsClosesImmediately(t *testing.T) {
	got := drain(t, New(nil, 0))
	if len(got) != 0 {
		t.Fatalf("got %v, want nothing", got)
	}
}
