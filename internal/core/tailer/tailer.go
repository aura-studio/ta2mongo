// Package tailer discovers log files by glob patterns and tails them,
// streaming new lines into a channel. It periodically rescans for new files.
package tailer

import (
	"bufio"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hpcloud/tail"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/log"
)

// ---------------------------------------------------------------------------
// Cross-platform path helpers (Linux ↔ Windows)
// ---------------------------------------------------------------------------

// driveLetterRe matches a Linux-style path starting with a single drive
// letter, e.g. "/c/..." or "/D/...".
var driveLetterRe = regexp.MustCompile(`^/([a-zA-Z])/`)

// linuxDriveRoot is the directory prefix where Windows drive letters are
// mounted on Linux (e.g. "/mnt/"). Detected once at init time.
// On Windows this is unused; on Linux without drive mounts it defaults
// to "/".
var linuxDriveRoot = detectDriveRoot()

func detectDriveRoot() string {
	if runtime.GOOS == "windows" {
		return "/" // unused on Windows, safe default
	}
	// Probe common Linux mount points; prefer /mnt/c, fall back to /c.
	for _, prefix := range []string{"/mnt/", "/"} {
		if fi, err := os.Stat(prefix + "c"); err == nil && fi.IsDir() {
			return prefix
		}
	}
	return "/"
}

// toWindowsPath converts a Linux-style path to a Windows path.
//
//	/c/xxx/ta.log  →  C:\xxx\ta.log
//	/var/log/...   →  unchanged (not a single-letter drive)
//	*.log          →  unchanged (relative pattern)
func toWindowsPath(path string) string {
	m := driveLetterRe.FindStringSubmatch(path)
	if m == nil {
		return path
	}
	drive := strings.ToUpper(m[1])
	rest := path[len(m[0]):]
	rest = strings.ReplaceAll(rest, "/", `\`)
	return drive + `:\` + rest
}

// normalizeWindowsPath converts a Windows file path to Linux-style format
// so that it can be matched against a Linux-style glob pattern.
//
//	C:\xxx\app.log  →  /c/xxx/app.log
func normalizeWindowsPath(path string) string {
	if len(path) >= 2 && path[1] == ':' {
		drive := strings.ToLower(string(path[0]))
		rest := path[2:]
		path = "/" + drive + rest
	}
	return strings.ReplaceAll(path, `\`, "/")
}

// toLinuxNativePath converts any path to Linux-native format, handling
// Windows drive letters and backslashes. It maps drive letters to the
// detected mount point (e.g. /mnt/c/).
//
//	C:\logs\app.log       →  /mnt/c/logs/app.log   (drives at /mnt/)
//	/c/logs/app.log       →  /mnt/c/logs/app.log   (drives at /mnt/)
//	/c/logs/app.log       →  /c/logs/app.log        (drives at /)
//	logs\app.log          →  logs/app.log
//	/var/log/app.log      →  /var/log/app.log       (unchanged)
func toLinuxNativePath(path string) string {
	// Handle Windows drive letter: C:\...
	if len(path) >= 2 && path[1] == ':' {
		drive := strings.ToLower(string(path[0]))
		rest := strings.ReplaceAll(path[2:], `\`, "/")
		return linuxDriveRoot + drive + rest
	}
	// Handle Linux-style drive letter: /c/...
	// Only remap when drives are at a prefix other than "/" (e.g. /mnt/).
	if linuxDriveRoot != "/" {
		if m := driveLetterRe.FindStringSubmatch(path); m != nil {
			drive := strings.ToLower(m[1])
			rest := path[len(m[0]):]
			return linuxDriveRoot + drive + "/" + rest
		}
	}
	// Just replace backslashes.
	return strings.ReplaceAll(path, `\`, "/")
}

// toNativePath converts a path to OS-native format for use with
// filepath.WalkDir and other filesystem calls.
//
//   - On Windows: /c/logs    →  C:\logs            (via toWindowsPath)
//   - On Linux:   C:\logs    →  /mnt/c/logs        (via toLinuxNativePath)
//   - On Linux:   /c/logs    →  /mnt/c/logs        (drive remapping)
//   - On Linux:   logs\app   →  logs/app           (backslash fix)
//
// Paths already in native format pass through unchanged.
func toNativePath(path string) string {
	if runtime.GOOS == "windows" {
		return toWindowsPath(path)
	}
	return toLinuxNativePath(path)
}

// normalizePath converts any file path to a canonical forward-slash format
// for glob matching. On all platforms, Windows-style paths are converted:
//
//   - On Windows:  C:\logs\app.log  →  /c/logs/app.log
//   - On Linux:    C:\logs\app.log  →  /mnt/c/logs/app.log
//   - On Linux:    /mnt/c/logs/x    →  /mnt/c/logs/x         (unchanged)
//   - Everywhere:  logs/app.log     →  logs/app.log           (unchanged)
func normalizePath(path string) string {
	if runtime.GOOS == "windows" {
		return normalizeWindowsPath(path)
	}
	return toLinuxNativePath(path)
}

// ---------------------------------------------------------------------------
// Glob matching (supports ** for recursive directory matching)
// ---------------------------------------------------------------------------

// globMatch matches name against a glob pattern. Both must use forward
// slashes. Supported wildcards:
//
//   - matches any sequence of non-separator characters
//     **     matches zero or more directory levels
//     ?      matches any single non-separator character
//     [...]  matches a character class
func globMatch(pattern, name string) bool {
	return doGlobMatch(
		strings.Split(pattern, "/"),
		strings.Split(name, "/"),
	)
}

// doGlobMatch performs segment-level recursive glob matching.
func doGlobMatch(patParts, nameParts []string) bool {
	for len(patParts) > 0 {
		seg := patParts[0]

		if seg == "**" {
			patParts = patParts[1:]
			// Consume consecutive ** segments.
			for len(patParts) > 0 && patParts[0] == "**" {
				patParts = patParts[1:]
			}
			if len(patParts) == 0 {
				return true // trailing ** matches everything
			}
			// Try matching remaining pattern at every depth.
			for i := 0; i <= len(nameParts); i++ {
				if doGlobMatch(patParts, nameParts[i:]) {
					return true
				}
			}
			return false
		}

		if len(nameParts) == 0 {
			return false
		}

		matched, _ := filepath.Match(seg, nameParts[0])
		if !matched {
			return false
		}

		patParts = patParts[1:]
		nameParts = nameParts[1:]
	}

	return len(nameParts) == 0
}

// ---------------------------------------------------------------------------
// Tailer
// ---------------------------------------------------------------------------

// defaultPollInterval is how often the tail loop re-reads a file after
// reaching EOF when no explicit interval is configured. A short interval
// avoids the notification-drop race present in event-driven watchers while
// keeping CPU usage negligible.
const defaultPollInterval = 200 * time.Millisecond

// defaultMaxLineSize is the maximum line length (bytes) the scanner accepts
// when none is configured.
const defaultMaxLineSize = 1 << 20 // 1 MiB

// Tailer watches for log files matching glob patterns and tails them,
// sending new lines to an output channel.
type Tailer struct {
	patterns       []string
	rescanInterval time.Duration
	tailMode       string // config.TailModePoll or config.TailModeEvent

	pollInterval time.Duration // EOF re-read cadence (poll/hybrid modes)
	maxLineSize  int           // scanner buffer cap per line

	mu     sync.Mutex
	tailed map[string]struct{} // tracks which files are already being tailed
}

// New creates a Tailer that watches the given glob patterns.
// tailMode selects the file-watching strategy: config.TailModePoll (default)
// or config.TailModeEvent. Tuning (poll interval, max line size) defaults to
// sane values; override with WithTuning.
func New(patterns []string, rescanInterval time.Duration, tailMode string) *Tailer {
	if tailMode == "" {
		tailMode = config.TailModePoll
	}
	return &Tailer{
		patterns:       patterns,
		rescanInterval: rescanInterval,
		tailMode:       tailMode,
		pollInterval:   defaultPollInterval,
		maxLineSize:    defaultMaxLineSize,
		tailed:         make(map[string]struct{}),
	}
}

// WithTuning overrides the poll interval and max line size. Non-positive
// values keep the current default. Returns the receiver for chaining.
func (t *Tailer) WithTuning(pollInterval time.Duration, maxLineBytes int) *Tailer {
	if pollInterval > 0 {
		t.pollInterval = pollInterval
	}
	if maxLineBytes > 0 {
		t.maxLineSize = maxLineBytes
	}
	return t
}

// Run discovers and tails files, sending lines to the returned channel.
// It blocks until ctx is cancelled, then closes the output channel.
// The caller should drain the channel after ctx cancellation.
func (t *Tailer) Run(ctx context.Context) <-chan string {
	out := make(chan string, 2000)

	go func() {
		defer close(out)
		t.run(ctx, out)
	}()

	return out
}

func (t *Tailer) run(ctx context.Context, out chan<- string) {
	// Initial file scan.
	t.scanAndTail(ctx, out)

	ticker := time.NewTicker(t.rescanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.scanAndTail(ctx, out)
		}
	}
}

// scanAndTail discovers files matching patterns and starts tailing new ones.
func (t *Tailer) scanAndTail(ctx context.Context, out chan<- string) {
	for _, path := range discoverFiles(t.patterns) {
		t.startFile(ctx, path, out)
	}
}

// startFile begins tailing a single file if not already tailed.
func (t *Tailer) startFile(ctx context.Context, path string, out chan<- string) {
	t.mu.Lock()
	if _, ok := t.tailed[path]; ok {
		t.mu.Unlock()
		return
	}
	t.tailed[path] = struct{}{}
	t.mu.Unlock()

	log.WithFields(log.Fields{
		"path":      path,
		"tail_mode": t.tailMode,
	}).Info("tailer: discovered and tailing new file")

	switch t.tailMode {
	case config.TailModeEvent:
		go t.tailFileEvent(ctx, path, out)
	case config.TailModePoll:
		go t.tailFile(ctx, path, out)
	default: // hybrid
		go t.tailFileHybrid(ctx, path, out)
	}
}

// tailFile reads a file from the beginning and follows new lines.
// It uses a simple poll-based approach: read until EOF, sleep briefly,
// then retry. This avoids the notification-drop race in hpcloud/tail's
// kqueue/inotify watcher where sendOnlyIfEmpty can silently discard
// a Modified event while the reader is busy, causing the tail to stall
// until the writer's next burst.
//
// File rotation (rename + recreate) is detected by comparing the file's
// inode across polls. When the underlying inode changes, the old file is
// closed and the new file is opened from the beginning.
func (t *Tailer) tailFile(ctx context.Context, path string, out chan<- string) {
	for {
		if err := t.readFollowFile(ctx, path, out); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.WithError(err).WithField("path", path).Warn("tailer: error reading file, will retry")
		}

		// File was deleted or rotated; wait for it to reappear.
		select {
		case <-ctx.Done():
			return
		case <-time.After(t.pollInterval):
		}
	}
}

// readFollowFile opens path, reads all lines, then polls for new data
// until the file is deleted/rotated or ctx is cancelled.
func (t *Tailer) readFollowFile(ctx context.Context, path string, out chan<- string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	origInfo, err := f.Stat()
	if err != nil {
		return err
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), t.maxLineSize)

	for {
		// Read all available complete lines.
		for scanner.Scan() {
			line := scanner.Text()
			if len(line) == 0 {
				continue
			}
			select {
			case out <- line:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}

		// EOF reached. Before sleeping, check if the file was rotated
		// (renamed and a new file created at the same path) by comparing
		// the current inode to the original.
		curInfo, err := os.Stat(path)
		if err != nil {
			// File removed; caller will wait and re-open.
			return err
		}
		if !os.SameFile(origInfo, curInfo) {
			// Inode changed → file was rotated. Return so caller reopens.
			return nil
		}

		// Wait before polling again.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(t.pollInterval):
		}

		// After the sleep the file position is still at the last read
		// offset. Create a fresh scanner that reuses the underlying fd
		// so it picks up any bytes appended since the last EOF.
		//
		// bufio.Scanner is not resumable after Scan returns false, so we
		// must create a new one. The file offset in f is preserved.
		//
		// We also need to handle the case where the writer wrote a
		// partial line (no trailing newline yet). Seek back to the start
		// of that partial line so the next scanner picks it up as a
		// complete line once more data is written.
		pos, err := f.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}

		// Check if the file was truncated (size shrank).
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if fi.Size() < pos {
			// File was truncated; rewind to beginning.
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return err
			}
		}

		scanner = bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), t.maxLineSize)
	}
}

// ---------------------------------------------------------------------------
// Event-driven tail (hpcloud/tail with kqueue/inotify)
// ---------------------------------------------------------------------------

// tailFileEvent uses hpcloud/tail's kqueue/inotify watcher to follow a file.
// It offers lower latency than polling but may stall under sustained
// concurrent writes due to the sendOnlyIfEmpty notification-drop race
// in the upstream library.
func (t *Tailer) tailFileEvent(ctx context.Context, path string, out chan<- string) {
	tt, err := tail.TailFile(path, tail.Config{
		Location:    &tail.SeekInfo{Whence: 0, Offset: 0},
		ReOpen:      true,
		Follow:      true,
		MustExist:   false,
		Poll:        false,
		Logger:      tail.DiscardingLogger,
		MaxLineSize: t.maxLineSize,
	})
	if err != nil {
		log.WithError(err).WithField("path", path).Warn("tailer: failed to start event-mode tailing")
		t.mu.Lock()
		delete(t.tailed, path)
		t.mu.Unlock()
		return
	}

	defer func() { _ = tt.Stop() }()

	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-tt.Lines:
			if !ok {
				return
			}
			if line == nil || len(line.Text) == 0 {
				continue
			}
			select {
			case out <- line.Text:
			case <-ctx.Done():
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Hybrid tail (event-driven + poll fallback)
// ---------------------------------------------------------------------------

// hybridPollInterval is how often the hybrid mode checks for missed
// notifications. Longer than pure-poll mode because the event watcher
// handles the common case; this is only a safety net.
const hybridPollInterval = 500 * time.Millisecond

// tailFileHybrid uses hpcloud/tail's event-driven watcher as the primary
// mechanism, with a periodic stat-based fallback. When the event watcher
// delivers a line within hybridPollInterval, it is forwarded immediately
// (low latency). If no line arrives before the ticker fires, the fallback
// stats the file: if the size grew since the last known position, it
// stops the stalled event watcher, reads the missed data with a polling
// reader, then restarts the event watcher from the new position.
//
// This combines the low latency of kqueue/inotify with the reliability
// of polling, eliminating the sendOnlyIfEmpty notification-drop race.
func (t *Tailer) tailFileHybrid(ctx context.Context, path string, out chan<- string) {
	// Track the last file size the event watcher has delivered up to.
	// We use this to detect whether the watcher has stalled.
	var lastSize int64
	if fi, err := os.Stat(path); err == nil {
		lastSize = fi.Size()
	}

	for {
		// --- Phase 1: event-driven reading via hpcloud/tail ---
		stalled := t.tailFileHybridEvent(ctx, path, out, &lastSize)
		if ctx.Err() != nil {
			return
		}

		if !stalled {
			// Event watcher exited normally (file deleted / rotated / error).
			// Wait briefly then re-enter the loop to reopen.
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.pollInterval):
			}
			continue
		}

		// --- Phase 2: poll fallback to drain missed data ---
		log.WithField("path", path).Debug("tailer: hybrid fallback — draining missed data via poll")
		if err := t.drainByPoll(ctx, path, out, &lastSize); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.WithError(err).WithField("path", path).Warn("tailer: hybrid poll drain error")
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.pollInterval):
			}
		}
		// Loop back to restart the event watcher from the updated position.
	}
}

// tailFileHybridEvent runs the event-driven watcher and returns true if
// the watcher stalled (no data despite file growth), false if it exited
// normally (file rotated, deleted, or error).
func (t *Tailer) tailFileHybridEvent(ctx context.Context, path string, out chan<- string, lastSize *int64) (stalled bool) {
	tt, err := tail.TailFile(path, tail.Config{
		Location:    &tail.SeekInfo{Whence: io.SeekStart, Offset: *lastSize},
		ReOpen:      true,
		Follow:      true,
		MustExist:   false,
		Poll:        false,
		Logger:      tail.DiscardingLogger,
		MaxLineSize: t.maxLineSize,
	})
	if err != nil {
		return false
	}
	defer func() { _ = tt.Stop() }()

	ticker := time.NewTicker(hybridPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return false

		case line, ok := <-tt.Lines:
			if !ok {
				return false
			}
			if line == nil || len(line.Text) == 0 {
				continue
			}
			select {
			case out <- line.Text:
			case <-ctx.Done():
				return false
			}
			// Update tracking position from the tail handle.
			if pos, err := tt.Tell(); err == nil {
				*lastSize = pos
			}

		case <-ticker.C:
			// Fallback check: did the file grow beyond what the event
			// watcher has delivered?
			fi, err := os.Stat(path)
			if err != nil {
				// File removed — let the event watcher handle it.
				continue
			}
			if fi.Size() > *lastSize {
				// Data was written but no event arrived — stalled.
				return true
			}
		}
	}
}

// drainByPoll reads data from *lastSize to the current EOF using the
// polling reader, then updates *lastSize.
func (t *Tailer) drainByPoll(ctx context.Context, path string, out chan<- string, lastSize *int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(*lastSize, io.SeekStart); err != nil {
		return err
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), t.maxLineSize)

	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		select {
		case out <- line:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	// Update position to current offset.
	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	*lastSize = pos
	return nil
}

// ---------------------------------------------------------------------------
// File discovery
// ---------------------------------------------------------------------------

// DiscoverFiles walks directories matching glob patterns and returns
// deduplicated file paths. This is exported for use by the once package.
func DiscoverFiles(patterns []string) []string {
	return discoverFiles(patterns)
}

// discoverFiles walks directories matching glob patterns and returns
// deduplicated file paths.
func discoverFiles(patterns []string) []string {
	seen := make(map[string]struct{})
	var result []string

	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}

		// Strip leading "./" or ".\" from pattern before matching,
		// because filepath.WalkDir never returns paths with "./" prefix.
		matchPattern := pattern
		if strings.HasPrefix(matchPattern, "./") || strings.HasPrefix(matchPattern, `.\`) {
			matchPattern = matchPattern[2:]
		}
		// Normalize the pattern to forward slashes so it matches the
		// normalized (forward-slash) paths returned by normalizePath.
		// Without this, Windows-style patterns like C:\logs\*.log would
		// fail to match against /c/logs/app.log.
		matchPattern = normalizePath(matchPattern)

		base := globBaseDir(pattern)
		log.WithFields(log.Fields{
			"pattern":  pattern,
			"walk_dir": base,
		}).Debug("tailer: scanning directory for matching files")

		var matched int
		walkErr := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip inaccessible paths
			}
			if d == nil || d.IsDir() {
				return nil
			}
			// Normalize the OS-native path to forward slashes so that the
			// glob pattern (written in Linux format) can match it.
			normalized := normalizePath(path)
			if globMatch(matchPattern, normalized) {
				if _, ok := seen[path]; !ok {
					seen[path] = struct{}{}
					result = append(result, path)
					matched++
				}
			}
			return nil
		})
		if walkErr != nil {
			log.WithError(walkErr).WithField("walk_dir", base).Warn("tailer: error walking directory")
		}

		log.WithFields(log.Fields{
			"pattern":       pattern,
			"walk_dir":      base,
			"files_matched": matched,
		}).Info("tailer: scan complete")
	}
	return result
}

// globBaseDir derives a walk root directory from a glob pattern by taking
// the literal prefix before the first glob metacharacter (*, ?, [).
//
// It handles both absolute and relative paths:
//
//	/var/log/app/*.log   →  /var/log/app     (absolute, Linux)
//	/c/xxx/ta.*.log      →  C:\xxx           (absolute, Windows via toNativePath)
//	logs/ta.*.log        →  logs             (relative)
//	./logs/*.log         →  logs             (relative with dot prefix)
//	**/*.log             →  .                (no literal prefix → current dir)
//	*.log                →  .                (no literal prefix → current dir)
func globBaseDir(pattern string) string {
	// Handle "./" or ".\" prefix: skip it when scanning for metacharacters
	// so that the leading dot is not mistaken for a glob metacharacter.
	dotPrefix := ""
	scan := pattern
	if strings.HasPrefix(scan, "./") || strings.HasPrefix(scan, `.\`) {
		dotPrefix = scan[:2]
		scan = scan[2:]
	}

	idx := strings.IndexAny(scan, "*?[")

	if idx < 0 {
		// No metacharacters — the entire pattern is a literal path.
		full := dotPrefix + scan
		full = strings.TrimRight(full, `/\`)
		if full == "" || full == "." {
			return "."
		}
		native := toNativePath(full)
		return filepath.Dir(native)
	}

	// Find the last directory separator before the first metacharacter.
	// Everything up to that separator is the walk root.
	prefix := scan[:idx]
	lastSep := strings.LastIndexAny(prefix, `/\`)
	if lastSep >= 0 {
		dir := dotPrefix + prefix[:lastSep]
		if dir == "" {
			return "."
		}
		return toNativePath(dir)
	}

	// No separator before metachar — the metachar is in the first segment.
	if dotPrefix != "" {
		return "."
	}
	return "."
}
