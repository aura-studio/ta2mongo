package tailer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

// ---------------------------------------------------------------------------
// regexBaseDir tests
// ---------------------------------------------------------------------------

func TestRegexBaseDir(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    string
	}{
		{
			name:    "pure literal path",
			pattern: "/var/log/app/access.log",
			want:    "/var/log/app",
		},
		{
			name:    "regex with dot-star after dir",
			pattern: "/var/log/app/.*\\.log",
			// '.' is a metachar, prefix="/var/log/app/", trimmed="/var/log/app", Dir -> "/var/log"
			want: "/var/log",
		},
		{
			name:    "regex starts at first component",
			pattern: "/var/log/.*/access\\.log",
			// '.' at "/var/log/.", prefix="/var/log/", trimmed="/var/log", Dir -> "/var"
			want: "/var",
		},
		{
			name:    "metachar at beginning",
			pattern: ".*\\.log",
			want:    string(filepath.Separator),
		},
		{
			name:    "bracket expression in filename",
			pattern: "/data/logs/[0-9]+\\.log",
			// '[' is a metachar, prefix="/data/logs/", trimmed="/data/logs", Dir -> "/data"
			want: "/data",
		},
		{
			name:    "question mark metachar",
			pattern: "/data/logs/app?.log",
			want:    "/data/logs",
		},
		{
			name:    "plus metachar",
			pattern: "/data/logs/app+\\.log",
			want:    "/data/logs",
		},
		{
			name:    "parentheses group",
			pattern: "/data/logs/(access|error)\\.log",
			// '(' is a metachar, prefix="/data/logs/", trimmed="/data/logs", Dir -> "/data"
			want: "/data",
		},
		{
			name:    "pipe alternation without prefix",
			pattern: "(access|error)\\.log",
			want:    string(filepath.Separator),
		},
		{
			name:    "caret anchor",
			pattern: "^/var/log/access\\.log",
			want:    string(filepath.Separator),
		},
		{
			name:    "trailing slash before metachar",
			pattern: "/var/log/.*",
			// '.' is a metachar, prefix="/var/log/", trimmed="/var/log", Dir -> "/var"
			want: "/var",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := regexBaseDir(tt.pattern)
			if got != tt.want {
				t.Errorf("regexBaseDir(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// discoverFiles / pattern matching tests
// ---------------------------------------------------------------------------

func TestDiscoverFiles_BasicPattern(t *testing.T) {
	dir := t.TempDir()

	// Create some test files.
	files := []string{
		filepath.Join(dir, "access.log"),
		filepath.Join(dir, "error.log"),
		filepath.Join(dir, "app.txt"),
	}
	for _, f := range files {
		if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	// Pattern matches only .log files.
	pattern := dir + "/.*\\.log"
	result := discoverFiles([]string{pattern}, logger)

	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(result), result)
	}

	// Verify both .log files are found.
	found := map[string]bool{}
	for _, r := range result {
		found[r] = true
	}
	for _, expect := range files[:2] {
		if !found[expect] {
			t.Errorf("expected %q in results, got %v", expect, result)
		}
	}
	// app.txt should not be in results.
	if found[files[2]] {
		t.Errorf("app.txt should not match .log pattern, got %v", result)
	}
}

func TestDiscoverFiles_EmptyPattern(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	result := discoverFiles([]string{""}, logger)
	if len(result) != 0 {
		t.Errorf("expected 0 files for empty pattern, got %d: %v", len(result), result)
	}
}

func TestDiscoverFiles_InvalidRegex(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	// Unclosed bracket is invalid regex.
	result := discoverFiles([]string{"[invalid"}, logger)
	if len(result) != 0 {
		t.Errorf("expected 0 files for invalid regex, got %d: %v", len(result), result)
	}
}

func TestDiscoverFiles_Deduplication(t *testing.T) {
	dir := t.TempDir()

	f := filepath.Join(dir, "access.log")
	if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	// Two patterns that match the same file.
	p1 := dir + "/.*\\.log"
	p2 := dir + "/access\\.log"
	result := discoverFiles([]string{p1, p2}, logger)

	if len(result) != 1 {
		t.Errorf("expected 1 deduplicated file, got %d: %v", len(result), result)
	}
}

func TestDiscoverFiles_SubdirectoryPattern(t *testing.T) {
	dir := t.TempDir()

	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "deep.log")
	if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	// Pattern should match files in subdirectories.
	pattern := dir + "/.*\\.log"
	result := discoverFiles([]string{pattern}, logger)

	if len(result) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(result), result)
	}
	if result[0] != f {
		t.Errorf("expected %q, got %q", f, result[0])
	}
}

func TestDiscoverFiles_MultiplePatterns(t *testing.T) {
	dir := t.TempDir()

	logFile := filepath.Join(dir, "app.log")
	txtFile := filepath.Join(dir, "data.txt")
	csvFile := filepath.Join(dir, "report.csv")
	for _, f := range []string{logFile, txtFile, csvFile} {
		if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	// Two different patterns matching different files.
	patterns := []string{
		dir + "/.*\\.log",
		dir + "/.*\\.txt",
	}
	result := discoverFiles(patterns, logger)

	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(result), result)
	}

	found := map[string]bool{}
	for _, r := range result {
		found[r] = true
	}
	if !found[logFile] {
		t.Errorf("expected %q in results", logFile)
	}
	if !found[txtFile] {
		t.Errorf("expected %q in results", txtFile)
	}
	if found[csvFile] {
		t.Errorf("csv file should not match, got %v", result)
	}
}

func TestDiscoverFiles_NonexistentDirectory(t *testing.T) {
	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	// Pattern pointing to a directory that does not exist.
	result := discoverFiles([]string{"/nonexistent/dir/.*\\.log"}, logger)
	if len(result) != 0 {
		t.Errorf("expected 0 files for nonexistent dir, got %d: %v", len(result), result)
	}
}

// ---------------------------------------------------------------------------
// Rescan tests
// ---------------------------------------------------------------------------

func TestRescan_PicksUpNewFiles(t *testing.T) {
	dir := t.TempDir()

	// Create an initial file.
	f1 := filepath.Join(dir, "first.log")
	if err := os.WriteFile(f1, []byte("line1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := dir + "/.*\\.log"
	tailer := New([]string{pattern}, 100*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := tailer.Run(ctx)

	// Wait for initial scan to complete.
	time.Sleep(200 * time.Millisecond)

	// tailer should have discovered the first file.
	tailer.mu.Lock()
	initialCount := len(tailer.tailed)
	tailer.mu.Unlock()

	if initialCount != 1 {
		t.Fatalf("expected 1 tailed file after initial scan, got %d", initialCount)
	}

	// Create a new file after the initial scan.
	f2 := filepath.Join(dir, "second.log")
	if err := os.WriteFile(f2, []byte("line2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for rescan (interval is 100ms, wait enough for at least 2 ticks).
	time.Sleep(400 * time.Millisecond)

	tailer.mu.Lock()
	afterRescanCount := len(tailer.tailed)
	tailer.mu.Unlock()

	if afterRescanCount != 2 {
		t.Errorf("expected 2 tailed files after rescan, got %d", afterRescanCount)
	}

	cancel()
	// Drain the channel to avoid goroutine leaks.
	for range out {
	}
}

func TestRescan_DoesNotDuplicateExistingFiles(t *testing.T) {
	dir := t.TempDir()

	f := filepath.Join(dir, "app.log")
	if err := os.WriteFile(f, []byte("data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := dir + "/.*\\.log"
	tailer := New([]string{pattern}, 100*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := tailer.Run(ctx)

	// Wait for initial scan + a couple of rescans.
	time.Sleep(500 * time.Millisecond)

	tailer.mu.Lock()
	count := len(tailer.tailed)
	tailCount := len(tailer.tails)
	tailer.mu.Unlock()

	if count != 1 {
		t.Errorf("expected 1 tailed entry, got %d", count)
	}
	if tailCount != 1 {
		t.Errorf("expected 1 tail handle, got %d", tailCount)
	}

	cancel()
	for range out {
	}
}

func TestRescan_StreamsNewLinesFromNewFile(t *testing.T) {
	dir := t.TempDir()

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := dir + "/.*\\.log"
	tailer := New([]string{pattern}, 100*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := tailer.Run(ctx)

	// Wait for initial (empty) scan.
	time.Sleep(150 * time.Millisecond)

	// Create a new file that the rescan should pick up. Since tailing starts
	// from the end of the file, we need to write *after* tailing starts.
	f := filepath.Join(dir, "new.log")
	if err := os.WriteFile(f, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Wait for rescan to discover and start tailing the file.
	time.Sleep(300 * time.Millisecond)

	// Now append a line to the file -- the tailer should stream it.
	fd, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fd.WriteString("hello from rescan\n")
	fd.Close()

	// Read from channel with timeout.
	select {
	case line := <-out:
		if line != "hello from rescan" {
			t.Errorf("unexpected line: %q", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for line from new file")
	}

	cancel()
	for range out {
	}
}
