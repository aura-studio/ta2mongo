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
// toWindowsPath / normalizeWindowsPath tests
// ---------------------------------------------------------------------------

func TestToWindowsPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"drive c", "/c/xxx/ta.*.log", `C:\xxx\ta.*.log`},
		{"drive d", "/d/logs/app.log", `D:\logs\app.log`},
		{"drive upper", "/D/data/logs/test.log", `D:\data\logs\test.log`},
		{"spaces in path", "/c/Program Files/app/*.log", `C:\Program Files\app\*.log`},
		{"no drive letter", "*.log", "*.log"},
		{"linux absolute", "/var/log/app/*.log", "/var/log/app/*.log"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toWindowsPath(tt.input)
			if got != tt.want {
				t.Errorf("toWindowsPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeWindowsPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"drive c", `C:\xxx\ta.2024.log`, "/c/xxx/ta.2024.log"},
		{"drive d", `D:\logs\app.log`, "/d/logs/app.log"},
		{"with spaces", `C:\Program Files\app\error.log`, "/c/Program Files/app/error.log"},
		{"already forward slash", "/var/log/app.log", "/var/log/app.log"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeWindowsPath(tt.input)
			if got != tt.want {
				t.Errorf("normalizeWindowsPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRoundTrip(t *testing.T) {
	cases := []string{
		"/c/xxx/ta.log",
		"/d/logs/app.log",
		"/e/data/deep/file.txt",
	}
	for _, c := range cases {
		win := toWindowsPath(c)
		back := normalizeWindowsPath(win)
		if back != c {
			t.Errorf("roundtrip(%q): toWindows=%q, back=%q", c, win, back)
		}
	}
}

// ---------------------------------------------------------------------------
// globMatch tests
// ---------------------------------------------------------------------------

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		// Basic * matching
		{"star matches filename", "logs/*.log", "logs/app.log", true},
		{"star no match extension", "logs/*.log", "logs/app.txt", false},
		{"star matches prefix", "logs/ta.*.log", "logs/ta.2024.log", true},
		{"star does not cross slash", "logs/*.log", "logs/sub/app.log", false},

		// ** recursive matching
		{"doublestar matches zero levels", "/var/log/**/*.log", "/var/log/app.log", true},
		{"doublestar matches one level", "/var/log/**/*.log", "/var/log/sub/app.log", true},
		{"doublestar matches deep", "/var/log/**/*.log", "/var/log/a/b/c/app.log", true},
		{"doublestar no match ext", "/var/log/**/*.log", "/var/log/sub/app.txt", false},
		{"leading doublestar", "**/*.log", "any/path/app.log", true},
		{"trailing doublestar", "/var/log/**", "/var/log/a/b/c.txt", true},
		{"doublestar alone", "**", "any/path/file.log", true},

		// ? single char matching
		{"question mark match", "logs/app?.log", "logs/app1.log", true},
		{"question mark no match", "logs/app?.log", "logs/app12.log", false},

		// [...] character class
		{"bracket match", "logs/app[0-9].log", "logs/app3.log", true},
		{"bracket no match", "logs/app[0-9].log", "logs/appx.log", false},

		// Absolute paths
		{"absolute match", "/var/log/app/*.log", "/var/log/app/access.log", true},
		{"absolute no match", "/var/log/app/*.log", "/var/log/other/access.log", false},

		// Windows-style paths (after normalization to forward slashes)
		{"win drive path", "/c/xxx/*.log", "/c/xxx/ta.2024.log", true},
		{"win drive deep", "/c/logs/**/*.log", "/c/logs/sub/app.log", true},

		// Edge cases
		{"exact match", "app.log", "app.log", true},
		{"no match", "app.log", "other.log", false},
		{"empty pattern empty path", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := globMatch(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("globMatch(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// globBaseDir tests
// ---------------------------------------------------------------------------

func TestGlobBaseDir(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		want    string
	}{
		// Absolute paths
		{
			name:    "pure literal path",
			pattern: "/var/log/app/access.log",
			want:    "/var/log/app",
		},
		{
			name:    "star in filename",
			pattern: "/var/log/app/*.log",
			want:    "/var/log/app",
		},
		{
			name:    "doublestar in middle",
			pattern: "/var/log/**/*.log",
			want:    "/var/log",
		},
		{
			name:    "star at beginning",
			pattern: "*.log",
			want:    ".",
		},
		{
			name:    "doublestar at beginning",
			pattern: "**/*.log",
			want:    ".",
		},
		{
			name:    "bracket in filename",
			pattern: "/data/logs/app[0-9].log",
			want:    "/data/logs",
		},
		{
			name:    "question mark in filename",
			pattern: "/data/logs/app?.log",
			want:    "/data/logs",
		},
		{
			name:    "trailing star",
			pattern: "/var/log/*",
			want:    "/var/log",
		},
		// Relative paths
		{
			name:    "relative with star",
			pattern: "logs/ta.*.log",
			want:    "logs",
		},
		{
			name:    "relative dot-slash",
			pattern: "./logs/*.log",
			want:    "./logs",
		},
		{
			name:    "relative dot-slash deep",
			pattern: "./data/logs/app[0-9].log",
			want:    "./data/logs",
		},
		{
			name:    "relative multi-level literal",
			pattern: "data/logs/app.log",
			want:    "data/logs",
		},
		{
			name:    "relative dot-slash with doublestar",
			pattern: "./**/*.log",
			want:    ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// globBaseDir may return OS-native separators (via
			// filepath.Dir / toNativePath), so normalise to forward
			// slashes for a platform-independent comparison.
			got := filepath.ToSlash(globBaseDir(tt.pattern))
			if got != tt.want {
				t.Errorf("globBaseDir(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// discoverFiles tests (using glob patterns)
// ---------------------------------------------------------------------------

func TestDiscoverFiles_BasicPattern(t *testing.T) {
	dir := t.TempDir()

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

	// Glob pattern matches only .log files.
	pattern := dir + "/*.log"
	result := discoverFiles([]string{pattern}, logger)

	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(result), result)
	}

	found := map[string]bool{}
	for _, r := range result {
		found[r] = true
	}
	for _, expect := range files[:2] {
		if !found[expect] {
			t.Errorf("expected %q in results, got %v", expect, result)
		}
	}
	if found[files[2]] {
		t.Errorf("app.txt should not match *.log pattern, got %v", result)
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

func TestDiscoverFiles_Deduplication(t *testing.T) {
	dir := t.TempDir()

	f := filepath.Join(dir, "access.log")
	if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	// Two patterns that match the same file.
	p1 := dir + "/*.log"
	p2 := dir + "/access.log"
	result := discoverFiles([]string{p1, p2}, logger)

	if len(result) != 1 {
		t.Errorf("expected 1 deduplicated file, got %d: %v", len(result), result)
	}
}

func TestDiscoverFiles_SubdirectoryWithDoublestar(t *testing.T) {
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

	// ** should match files in subdirectories.
	pattern := dir + "/**/*.log"
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

	patterns := []string{
		dir + "/*.log",
		dir + "/*.txt",
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

	result := discoverFiles([]string{"/nonexistent/dir/*.log"}, logger)
	if len(result) != 0 {
		t.Errorf("expected 0 files for nonexistent dir, got %d: %v", len(result), result)
	}
}

func TestDiscoverFiles_RelativePath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "logs")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	f1 := filepath.Join(sub, "ta.2024.log")
	f2 := filepath.Join(sub, "ta.2025.log")
	f3 := filepath.Join(sub, "readme.txt")
	for _, f := range []string{f1, f2, f3} {
		if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := "logs/ta.*.log"
	result := discoverFiles([]string{pattern}, logger)

	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(result), result)
	}
}

func TestDiscoverFiles_DotSlashRelativePath(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "logs")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "app.log")
	if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := "./logs/*.log"
	result := discoverFiles([]string{pattern}, logger)

	if len(result) != 1 {
		t.Fatalf("expected 1 file, got %d: %v", len(result), result)
	}
}

func TestDiscoverFiles_DoublestarRelative(t *testing.T) {
	dir := t.TempDir()
	deep := filepath.Join(dir, "a", "b", "c")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(deep, "app.log")
	if err := os.WriteFile(f, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	// Also create a top-level log.
	fTop := filepath.Join(dir, "top.log")
	if err := os.WriteFile(fTop, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := "**/*.log"
	result := discoverFiles([]string{pattern}, logger)

	if len(result) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(result), result)
	}
}

// ---------------------------------------------------------------------------
// Rescan tests
// ---------------------------------------------------------------------------

func TestRescan_PicksUpNewFiles(t *testing.T) {
	dir := t.TempDir()

	f1 := filepath.Join(dir, "first.log")
	if err := os.WriteFile(f1, []byte("line1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := dir + "/*.log"
	tailer := New([]string{pattern}, 100*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := tailer.Run(ctx)

	time.Sleep(200 * time.Millisecond)

	tailer.mu.Lock()
	initialCount := len(tailer.tailed)
	tailer.mu.Unlock()

	if initialCount != 1 {
		t.Fatalf("expected 1 tailed file after initial scan, got %d", initialCount)
	}

	f2 := filepath.Join(dir, "second.log")
	if err := os.WriteFile(f2, []byte("line2\n"), 0644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(400 * time.Millisecond)

	tailer.mu.Lock()
	afterRescanCount := len(tailer.tailed)
	tailer.mu.Unlock()

	if afterRescanCount != 2 {
		t.Errorf("expected 2 tailed files after rescan, got %d", afterRescanCount)
	}

	cancel()
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

	pattern := dir + "/*.log"
	tailer := New([]string{pattern}, 100*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := tailer.Run(ctx)

	time.Sleep(500 * time.Millisecond)

	tailer.mu.Lock()
	count := len(tailer.tailed)
	tailer.mu.Unlock()

	if count != 1 {
		t.Errorf("expected 1 tailed entry, got %d", count)
	}

	cancel()
	for range out {
	}
}

// TestContinuousWrite_TailerKeepsUp simulates the scenario where lumberjack
// (or any writer) continuously appends lines to a file while the tailer is
// reading. The tailer must not get stuck.
func TestContinuousWrite_TailerKeepsUp(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "app.log")

	// Create the file first.
	if err := os.WriteFile(logFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := dir + "/*.log"
	tailer := New([]string{pattern}, 30*time.Second, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := tailer.Run(ctx)

	// Wait for tailer to discover and start tailing.
	time.Sleep(200 * time.Millisecond)

	// Simulate lumberjack: open in O_APPEND|O_WRONLY and write rapidly
	// in bursts, keeping the fd open the entire time (exactly how
	// lumberjack behaves).
	totalLines := 5000
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		fd, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			t.Errorf("writer: open failed: %v", err)
			return
		}
		defer fd.Close()

		for i := 0; i < totalLines; i++ {
			_, _ = fd.WriteString("line content here\n")
			// Write in bursts: 50 lines, then 1ms pause.
			if i%50 == 49 {
				time.Sleep(1 * time.Millisecond)
			}
		}
	}()

	// Drain the tailer output and count received lines.
	received := 0
	deadline := time.After(30 * time.Second)
	for received < totalLines {
		select {
		case _, ok := <-out:
			if !ok {
				t.Fatalf("tailer output closed after %d lines (expected %d)", received, totalLines)
			}
			received++
		case <-deadline:
			t.Fatalf("tailer stuck: received only %d/%d lines", received, totalLines)
		}
	}

	<-writerDone
	cancel()
	for range out {
	}
}

// TestContinuousWrite_SlowConsumer simulates the real-world scenario:
// lumberjack writes rapidly while the tailer's consumer (pipeline) is slow.
// When the output channel fills up, the tailer's readLines goroutine blocks
// on `out <- line.Text`. This means hpcloud/tail's `sendLine()` blocks on
// `tail.Lines <- &Line{...}` (unbuffered channel), stalling the tail loop.
// The tail loop can't call waitForChanges(), so kqueue notifications pile up
// and get dropped by sendOnlyIfEmpty. After the consumer resumes (or the
// writer stops), the tailer must recover.
func TestContinuousWrite_SlowConsumer(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "app.log")

	if err := os.WriteFile(logFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := dir + "/*.log"
	tailer := New([]string{pattern}, 30*time.Second, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := tailer.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	// Write 100 lines rapidly, then stop (simulating lumberjack stopping).
	totalLines := 100
	fd, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < totalLines; i++ {
		_, _ = fd.WriteString("line content here\n")
	}
	fd.Close()

	// Don't read from out for 2 seconds — simulate slow pipeline consumer.
	// During this time the tailer's out channel (cap=2000) fills, and the
	// hpcloud/tail internal Lines channel (unbuffered) causes backpressure.
	time.Sleep(2 * time.Second)

	// Now write more lines AFTER the pause — these are the ones that may
	// be missed if the notification was dropped.
	fd2, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	moreLines := 50
	for i := 0; i < moreLines; i++ {
		_, _ = fd2.WriteString("after-pause line\n")
	}
	fd2.Close()

	// Now drain everything. We should get all totalLines + moreLines.
	received := 0
	expected := totalLines + moreLines
	deadline := time.After(10 * time.Second)
	for received < expected {
		select {
		case _, ok := <-out:
			if !ok {
				t.Fatalf("tailer output closed after %d lines (expected %d)", received, expected)
			}
			received++
		case <-deadline:
			t.Fatalf("tailer stuck: received only %d/%d lines", received, expected)
		}
	}

	cancel()
	for range out {
	}
}

func TestRescan_StreamsNewLinesFromNewFile(t *testing.T) {
	dir := t.TempDir()

	logger := logrus.New()
	logger.SetOutput(os.Stderr)

	pattern := dir + "/*.log"
	tailer := New([]string{pattern}, 100*time.Millisecond, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := tailer.Run(ctx)

	time.Sleep(150 * time.Millisecond)

	f := filepath.Join(dir, "new.log")
	if err := os.WriteFile(f, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	time.Sleep(300 * time.Millisecond)

	fd, err := os.OpenFile(f, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fd.WriteString("hello from rescan\n")
	fd.Close()

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
