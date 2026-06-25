// Package file is the one-shot file-import source: it reads every line of each
// EXPLICITLY-LISTED file from start to EOF, then closes the channel. It is the
// finite counterpart of the tailer (which keeps following files for new lines)
// and the file counterpart of the cli's stdin source — the source behind the
// "file" face that bulk-imports already-on-disk log files through the regular
// upload pipeline.
//
// Inputs are explicit file paths only: there is NO glob expansion, NO directory
// walking, and NO dependency on the tailer's file discovery. A path that is a
// directory (or is missing / unreadable) is logged and skipped — directories
// are never expanded.
//
// There is no checkpoint and no resume: re-running re-imports everything, and
// idempotency is owned by the write models per operation type — events upsert
// by uuid (zero new docs) and user_set-style ops converge, while accumulating
// ops (user_add/$inc, user_append/$push) re-apply on every run because the
// _ts guard orders writes but does not deduplicate replays.
package file

import (
	"bufio"
	"context"
	"os"

	"github.com/aura-studio/tango/internal/logging"
)

// defaultMaxLineSize is the maximum line length (bytes) the scanner accepts
// when none is configured (10 MiB).
const defaultMaxLineSize = 10 * 1024 * 1024

// Source imports each explicitly-listed file once.
type Source struct {
	paths       []string
	maxLineSize int
}

// New creates a Source that imports the given explicit file paths. Paths are
// taken verbatim — no glob, no directories. A non-positive maxLineBytes keeps
// the default (10 MiB).
func New(paths []string, maxLineBytes int) *Source {
	if maxLineBytes <= 0 {
		maxLineBytes = defaultMaxLineSize
	}
	return &Source{paths: paths, maxLineSize: maxLineBytes}
}

// Run streams each listed file's non-empty lines from start to EOF, in the
// given order, then closes the channel. A path that is a directory, is missing,
// or cannot be read (or whose scan dies on an over-long line) is logged and
// skipped — the remaining files still import. Directories are NOT expanded.
// Cancelling ctx closes the channel early.
func (s *Source) Run(ctx context.Context) <-chan string {
	out := make(chan string, 2000)

	go func() {
		defer close(out)
		defer logging.Recover("file source")

		var lines int64
		for _, path := range s.paths {
			if ctx.Err() != nil {
				return
			}
			info, err := os.Stat(path)
			if err != nil {
				logging.WithError(err).WithField("path", path).
					Warn("file: cannot stat path, skipping")
				continue
			}
			if info.IsDir() {
				logging.WithField("path", path).
					Warn("file: path is a directory, skipping (directories are not supported)")
				continue
			}
			n, err := s.streamFile(ctx, path, out)
			lines += n
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				logging.WithError(err).WithField("path", path).
					Warn("file: error reading file, skipping its remainder")
			}
		}
		logging.WithFields(logging.Fields{
			"files": len(s.paths),
			"lines": lines,
		}).Info("file: finished streaming")
	}()

	return out
}

// streamFile reads path from the beginning and emits every non-empty line,
// returning the number of lines emitted. A 64 KiB initial buffer capped at
// maxLineSize means an over-long line aborts the scan with bufio.ErrTooLong
// (surfaced to the caller) without emitting the oversized token.
func (s *Source) streamFile(ctx context.Context, path string, out chan<- string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), s.maxLineSize)

	var n int64
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) == 0 {
			continue
		}
		select {
		case out <- line:
			n++
		case <-ctx.Done():
			return n, ctx.Err()
		}
	}
	return n, scanner.Err()
}
