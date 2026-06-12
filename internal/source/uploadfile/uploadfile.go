// Package uploadfile is the one-shot file-import source: it discovers the
// files matching its glob patterns once, streams every line of every file from
// start to EOF, then closes the channel. It is the finite counterpart of the
// tailer (which keeps following files for new lines) and the file counterpart
// of the cli's stdin source — the source behind the uploadfile face that bulk
// imports already-on-disk log files through the regular upload pipeline.
//
// There is no checkpoint and no resume: re-running re-imports everything, and
// idempotency is owned by the write model (events upsert by uuid, user fields
// are _ts-guarded), so a re-import converges to the same state.
package uploadfile

import (
	"bufio"
	"context"
	"os"

	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/source/tailer"
)

// defaultMaxLineSize is the maximum line length (bytes) the scanner accepts
// when none is configured. It matches the tailer's default so a one-shot
// import accepts exactly the lines the resident tailer would.
const defaultMaxLineSize = 10 * 1024 * 1024 // 10 MiB

// Source imports every file matching its glob patterns once.
type Source struct {
	patterns    []string
	maxLineSize int
}

// New creates a Source that imports the files matching the given glob
// patterns (the tailer's pattern syntax, including ** and cross-platform
// paths). A non-positive maxLineBytes keeps the default (10 MiB).
func New(patterns []string, maxLineBytes int) *Source {
	if maxLineBytes <= 0 {
		maxLineBytes = defaultMaxLineSize
	}
	return &Source{patterns: patterns, maxLineSize: maxLineBytes}
}

// Run discovers the matching files once, streams each file's non-empty lines
// from start to EOF in discovery order, then closes the channel. Patterns that
// match nothing yield a source that emits nothing. A file that cannot be read
// (or whose scan dies on an over-long line) is logged and skipped — the
// remaining files still import. Cancelling ctx closes the channel early.
func (s *Source) Run(ctx context.Context) <-chan string {
	out := make(chan string, 2000)

	go func() {
		defer close(out)
		defer logging.Recover("uploadfile source")

		files := tailer.DiscoverFiles(s.patterns)
		logging.WithFields(logging.Fields{
			"patterns": s.patterns,
			"files":    len(files),
		}).Info("uploadfile: discovered files to import")

		var lines int64
		for _, path := range files {
			if ctx.Err() != nil {
				return
			}
			n, err := s.streamFile(ctx, path, out)
			lines += n
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				logging.WithError(err).WithField("path", path).
					Warn("uploadfile: error reading file, skipping its remainder")
			}
		}
		logging.WithFields(logging.Fields{
			"files": len(files),
			"lines": lines,
		}).Info("uploadfile: finished streaming")
	}()

	return out
}

// streamFile reads path from the beginning and emits every non-empty line,
// returning the number of lines emitted. The scanner semantics mirror the
// tailer's: a 64 KiB initial buffer capped at maxLineSize, so an over-long
// line aborts the scan with bufio.ErrTooLong (surfaced to the caller) without
// emitting the oversized token.
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
