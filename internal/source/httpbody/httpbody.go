// Package httpbody wraps HTTP request body data as a source of log lines.
// It accepts pre-parsed lines (single string or batch) and emits them via a
// channel, satisfying the source.Source contract consumed by the process
// uploaders. It is the only source the gateway role uses.
//
// Usage:
//
//	src := httpbody.New(lines)
//	up, _ := process.New(cfg, dao, parser, stats, opts)
//	err := up.Run(ctx, src)
package httpbody

import (
	"context"

	"rocket-nano/tools/tango/internal/logging"
)

// Source wraps pre-parsed HTTP request body lines as a line source.
type Source struct {
	lines []string
}

// New creates a Source from a slice of log lines. Empty lines are skipped
// during emission. A nil or empty slice produces a source that immediately
// closes its output channel (no lines emitted).
func New(lines []string) *Source {
	return &Source{lines: lines}
}

// Run sends all non-empty lines to the returned channel, then closes it.
// It respects ctx cancellation.
func (s *Source) Run(ctx context.Context) <-chan string {
	out := make(chan string, len(s.lines)+1)
	logging.WithField("lines", len(s.lines)).Debug("httpbody: emitting request-body lines")
	go func() {
		defer close(out)
		defer logging.Recover("httpbody source")
		for _, line := range s.lines {
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
