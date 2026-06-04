package source

import (
	"io"

	"rocket-nano/tools/tango/internal/source/httpbody"
	"rocket-nano/tools/tango/internal/source/stdin"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// This file fronts the source subpackages with constructors so external layers
// (the role services and the process tests) build a Source through the source
// package alone, never importing source/httpbody, source/stdin or source/tailer
// directly — mirroring how dao fronts its store and parser fronts talog/filter.

// NewLines returns a finite Source that emits the given log lines (the
// in-memory / HTTP-request-body source). Empty lines are skipped; a nil or empty
// slice yields a source that emits nothing. Used by the api/gateway roles to
// wrap request bodies.
func NewLines(lines []string) Source { return httpbody.New(lines) }

// NewReader returns a Source that streams log lines from r, one per line, until
// EOF or context cancellation (the stdin/console source). A nil reader defaults
// to os.Stdin. Used by the cli role.
func NewReader(r io.Reader) Source { return stdin.New(r) }

// NewTailer returns the long-running file-tailing Source configured by cfg
// (source.tailer.*): it watches the configured glob patterns and streams newly
// appended lines until the context is cancelled. Used by the daemon role.
func NewTailer(cfg *tailer.Config) Source {
	if cfg == nil {
		cfg = &tailer.Config{}
	}
	return tailer.New(cfg.LogPattern, cfg.RescanInterval, cfg.TailMode).
		WithTuning(cfg.PollInterval, cfg.MaxLineBytes)
}
