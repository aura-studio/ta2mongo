package uploadfile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// uploadfile_test.go covers the one-shot file-import source per the v1.6.0
// acceptance list (doc/v1.6/requirements.md §3): glob discovery, line
// boundaries, maxLineBytes, ctx cancellation and empty matches.

// drain collects every value a Source emits until its channel closes, returning
// the emitted slice and whether the channel was observed closed within the
// deadline (the convention of internal/source/ultra_source_test.go).
func drain(t *testing.T, ch <-chan string, deadline time.Duration) (got []string, closed bool) {
	t.Helper()
	timeout := time.After(deadline)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return got, true
			}
			got = append(got, v)
		case <-timeout:
			return got, false
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------
// Glob discovery: only matching files import, every matching file imports,
// lines stream in per-file order with files in discovery (lexical) order.
// ---------------------------------------------------------------------------

func TestUploadFile_GlobMatchesAndStreamsInOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.log"), "a1\na2\n")
	writeFile(t, filepath.Join(dir, "b.log"), "b1\n")
	writeFile(t, filepath.Join(dir, "skip.txt"), "never\n")

	src := New([]string{filepath.Join(dir, "*.log")}, 0)
	got, closed := drain(t, src.Run(context.Background()), 5*time.Second)
	if !closed {
		t.Fatal("uploadfile channel did not close after finite import")
	}
	// filepath.WalkDir visits lexically, so a.log streams before b.log and
	// each file's lines keep their order.
	want := []string{"a1", "a2", "b1"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("emitted %q, want %q (only *.log, per-file order, lexical file order)", got, want)
	}
}

func TestUploadFile_GlobRecursiveDoubleStar(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "nested", "deeper")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "top.log"), "top\n")
	writeFile(t, filepath.Join(sub, "deep.log"), "deep\n")

	src := New([]string{filepath.Join(dir, "**", "*.log")}, 0)
	got, closed := drain(t, src.Run(context.Background()), 5*time.Second)
	if !closed {
		t.Fatal("uploadfile channel did not close")
	}
	if len(got) != 2 {
		t.Fatalf("emitted %q, want the 2 lines of top.log and nested/deeper/deep.log", got)
	}
}

// ---------------------------------------------------------------------------
// Line boundaries: empties skipped, CRLF stripped, an unterminated final line
// is still emitted — the tailer/stdin scanner semantics.
// ---------------------------------------------------------------------------

func TestUploadFile_LineBoundaries(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "lines.log"), "one\n\ntwo\r\n\n\nthree")

	src := New([]string{filepath.Join(dir, "*.log")}, 0)
	got, closed := drain(t, src.Run(context.Background()), 5*time.Second)
	if !closed {
		t.Fatal("uploadfile channel did not close")
	}
	want := []string{"one", "two", "three"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("emitted %q, want %q (empties skipped, \\r stripped, final unterminated line kept)", got, want)
	}
}

// ---------------------------------------------------------------------------
// maxLineBytes: an over-max line aborts that file's scan (the oversized token
// and the file's remainder are dropped, like the stdin source), but the other
// matched files still import — per-file isolation.
//
// Note bufio.Scanner's cap floor: the 64 KiB initial buffer means only a
// maxLineBytes above 64 KiB is effective — the same semantics as the tailer,
// whose scanner this source mirrors.
// ---------------------------------------------------------------------------

func TestUploadFile_MaxLineBytesSkipsFileRemainderOnly(t *testing.T) {
	const maxLine = 100 * 1024 // > the 64 KiB scanner floor so the cap is live
	dir := t.TempDir()
	huge := strings.Repeat("x", maxLine+1024)
	writeFile(t, filepath.Join(dir, "a-bad.log"), "before\n"+huge+"\nafter\n")
	writeFile(t, filepath.Join(dir, "b-good.log"), "ok\n")

	src := New([]string{filepath.Join(dir, "*.log")}, maxLine)
	got, closed := drain(t, src.Run(context.Background()), 5*time.Second)
	if !closed {
		t.Fatal("uploadfile channel did not close after an over-max line")
	}
	// a-bad.log: "before" emitted, then ErrTooLong aborts the file — the
	// oversized token and "after" never appear. b-good.log still imports.
	want := []string{"before", "ok"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("emitted %d lines, want %q (over-max token + file remainder dropped, next file intact); got first/last %.20q",
			len(got), want, got)
	}
}

// ---------------------------------------------------------------------------
// ctx cancellation: with the producer parked on a full channel, cancel must
// close the channel promptly instead of leaking the goroutine.
// ---------------------------------------------------------------------------

func TestUploadFile_CtxCancelStops(t *testing.T) {
	dir := t.TempDir()
	// More lines than the 2000-slot channel buffer so the producer blocks.
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		b.WriteString("line\n")
	}
	writeFile(t, filepath.Join(dir, "big.log"), b.String())

	ctx, cancel := context.WithCancel(context.Background())
	ch := New([]string{filepath.Join(dir, "*.log")}, 0).Run(ctx)

	// Take a few lines, leave the producer parked on the blocking send.
	for i := 0; i < 100; i++ {
		<-ch
	}
	cancel()
	got, closed := drain(t, ch, 5*time.Second)
	if !closed {
		t.Fatal("uploadfile channel did not close after ctx cancel while producer was blocked")
	}
	if len(got) >= 2900 {
		t.Fatalf("drained %d more lines after cancel; producer should have stopped early", len(got))
	}
}

// ---------------------------------------------------------------------------
// Empty matches: patterns that match nothing — and no patterns at all — yield
// a source that closes immediately with zero lines.
// ---------------------------------------------------------------------------

func TestUploadFile_EmptyMatchEmitsNothing(t *testing.T) {
	dir := t.TempDir() // empty: pattern matches no files
	for _, patterns := range [][]string{
		{filepath.Join(dir, "*.log")},
		nil,
	} {
		got, closed := drain(t, New(patterns, 0).Run(context.Background()), 5*time.Second)
		if !closed {
			t.Fatalf("patterns %q: channel did not close", patterns)
		}
		if len(got) != 0 {
			t.Fatalf("patterns %q: emitted %q, want nothing", patterns, got)
		}
	}
}
