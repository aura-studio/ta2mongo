// Package stdin wraps an io.Reader (os.Stdin by default) as a source of log
// lines: it scans the reader line by line and emits each non-empty line on a
// channel, satisfying the source.Source contract. It is the source the cli role
// uses for console-driven ingestion.
package stdin

import (
	"bufio"
	"context"
	"io"
	"os"
)

// defaultMaxLineBytes caps a single scanned line (1 MiB) to bound buffer growth.
const defaultMaxLineBytes = 1 << 20

// Source reads log lines from an io.Reader.
type Source struct {
	r           io.Reader
	maxLineSize int
}

// New creates a Source reading from r. A nil r defaults to os.Stdin.
func New(r io.Reader) *Source {
	if r == nil {
		r = os.Stdin
	}
	return &Source{r: r, maxLineSize: defaultMaxLineBytes}
}

// Run scans r line by line, emitting each non-empty line, then closes the
// channel at EOF (or when ctx is cancelled).
func (s *Source) Run(ctx context.Context) <-chan string {
	out := make(chan string, 256)
	go func() {
		defer close(out)
		scanner := bufio.NewScanner(s.r)
		scanner.Buffer(make([]byte, 0, 64*1024), s.maxLineSize)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			select {
			case out <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
