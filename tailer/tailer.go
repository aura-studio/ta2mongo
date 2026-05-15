// Package tailer discovers log files by regex patterns and tails them,
// streaming new lines into a channel. It periodically rescans for new files.
package tailer

import (
	"context"
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hpcloud/tail"
	"github.com/sirupsen/logrus"
)

// Tailer watches for log files matching regex patterns and tails them
// from the end, sending new lines to an output channel.
type Tailer struct {
	patterns       []string
	rescanInterval time.Duration
	logger         *logrus.Logger

	mu     sync.Mutex
	tailed map[string]struct{}   // tracks which files are already being tailed
	tails  map[string]*tail.Tail // active tail handles for cleanup
}

// New creates a Tailer that watches the given regex patterns.
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

	// Start from file end (incremental, daemon mode).
	tt, err := tail.TailFile(path, tail.Config{
		Location:    &tail.SeekInfo{Whence: 2, Offset: 0},
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
// File discovery (replaces the old "matches" package)
// ---------------------------------------------------------------------------

// DiscoverFiles walks directories matching regex patterns and returns
// deduplicated file paths. This is exported for use by the once package.
func DiscoverFiles(patterns []string, logger *logrus.Logger) []string {
	return discoverFiles(patterns, logger)
}

// discoverFiles walks directories matching regex patterns and returns
// deduplicated file paths.
func discoverFiles(patterns []string, logger *logrus.Logger) []string {
	seen := make(map[string]struct{})
	var result []string

	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			logger.WithError(err).WithField("pattern", pattern).Warn("invalid logPattern regex, skipped")
			continue
		}

		base := regexBaseDir(pattern)
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip inaccessible paths
			}
			if d == nil || d.IsDir() {
				return nil
			}
			if re.MatchString(path) {
				if _, ok := seen[path]; !ok {
					seen[path] = struct{}{}
					result = append(result, path)
				}
			}
			return nil
		})
	}
	return result
}

// regexBaseDir derives a walk root directory from a regex pattern by taking
// the literal prefix before the first regex metacharacter.
func regexBaseDir(pattern string) string {
	const metas = `^$.*+?()[]{}|\`
	idx := -1
	for i, r := range pattern {
		if strings.ContainsRune(metas, r) {
			idx = i
			break
		}
	}

	prefix := pattern
	if idx >= 0 {
		prefix = pattern[:idx]
	}
	prefix = strings.TrimRight(prefix, `/\`)
	if prefix == "" {
		return string(filepath.Separator)
	}
	return filepath.Dir(prefix)
}
