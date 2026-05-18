// Package tailer discovers log files by glob patterns and tails them,
// streaming new lines into a channel. It periodically rescans for new files.
package tailer

import (
	"context"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/hpcloud/tail"
	"github.com/sirupsen/logrus"
)

// ---------------------------------------------------------------------------
// Cross-platform path helpers (Linux ↔ Windows)
// ---------------------------------------------------------------------------

// driveLetterRe matches a Linux-style path starting with a single drive
// letter, e.g. "/c/..." or "/D/...".
var driveLetterRe = regexp.MustCompile(`^/([a-zA-Z])/`)

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

// toNativePath converts a Linux-style path to OS-native format.
// On Windows it calls toWindowsPath; on other OS it returns input unchanged.
func toNativePath(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	return toWindowsPath(path)
}

// normalizePath converts an OS-native file path to Linux-style forward-slash
// format. On Windows it calls normalizeWindowsPath; on other OS returns input
// unchanged.
func normalizePath(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	return normalizeWindowsPath(path)
}

// ---------------------------------------------------------------------------
// Glob matching (supports ** for recursive directory matching)
// ---------------------------------------------------------------------------

// globMatch matches name against a glob pattern. Both must use forward
// slashes. Supported wildcards:
//
//	*      matches any sequence of non-separator characters
//	**     matches zero or more directory levels
//	?      matches any single non-separator character
//	[...]  matches a character class
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

// Tailer watches for log files matching glob patterns and tails them
// from the end, sending new lines to an output channel.
type Tailer struct {
	patterns       []string
	rescanInterval time.Duration
	logger         *logrus.Logger

	mu     sync.Mutex
	tailed map[string]struct{}   // tracks which files are already being tailed
	tails  map[string]*tail.Tail // active tail handles for cleanup
}

// New creates a Tailer that watches the given glob patterns.
func New(patterns []string, rescanInterval time.Duration, logger *logrus.Logger) *Tailer {
	return &Tailer{
		patterns:       patterns,
		rescanInterval: rescanInterval,
		logger:         logger,
		tailed:         make(map[string]struct{}),
		tails:          make(map[string]*tail.Tail),
	}
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
			t.stopAll()
			return
		case <-ticker.C:
			t.scanAndTail(ctx, out)
		}
	}
}

// scanAndTail discovers files matching patterns and starts tailing new ones.
func (t *Tailer) scanAndTail(ctx context.Context, out chan<- string) {
	for _, path := range discoverFiles(t.patterns, t.logger) {
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

	// Start from file beginning so existing content is processed on first run.
	tt, err := tail.TailFile(path, tail.Config{
		Location:    &tail.SeekInfo{Whence: 0, Offset: 0},
		ReOpen:      true,
		Follow:      true,
		MustExist:   false,
		Poll:        false,
		Logger:      tail.DiscardingLogger,
		MaxLineSize: 1 << 20, // 1 MiB
	})
	if err != nil {
		t.logger.WithError(err).WithField("path", path).Warn("failed to start tailing file")
		t.mu.Lock()
		delete(t.tailed, path)
		t.mu.Unlock()
		return
	}

	t.mu.Lock()
	t.tails[path] = tt
	t.mu.Unlock()

	t.logger.WithField("path", path).Info("tailer: discovered and tailing new file")

	go t.readLines(ctx, tt, out)
}

// readLines reads from a tail handle and forwards non-empty lines to out.
func (t *Tailer) readLines(ctx context.Context, tt *tail.Tail, out chan<- string) {
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

// stopAll cleanly shuts down all active tail handles.
func (t *Tailer) stopAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, tt := range t.tails {
		if tt != nil {
			_ = tt.Stop()
		}
	}
}

// ---------------------------------------------------------------------------
// File discovery
// ---------------------------------------------------------------------------

// DiscoverFiles walks directories matching glob patterns and returns
// deduplicated file paths. This is exported for use by the once package.
func DiscoverFiles(patterns []string, logger *logrus.Logger) []string {
	return discoverFiles(patterns, logger)
}

// discoverFiles walks directories matching glob patterns and returns
// deduplicated file paths.
func discoverFiles(patterns []string, logger *logrus.Logger) []string {
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
		logger.WithFields(logrus.Fields{
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
			logger.WithError(walkErr).WithField("walk_dir", base).Warn("tailer: error walking directory")
		}

		logger.WithFields(logrus.Fields{
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
