// Package source is the data-source layer: each subpackage adapts an external
// origin of log lines (HTTP request body, file tailing, stdin, the ThinkingData
// API) into the common Source contract consumed by the process uploaders.
//
// A Source is anything that, when Run, streams log lines onto a channel and
// closes it when finished (or when the context is cancelled). The process
// package's three upload strategies (single / batch / pipeline) all consume a
// Source; the gateway role wraps each HTTP request body via source/httpbody,
// while the daemon role tails files via source/tailer.
package source

import (
	"context"
	"io"

	"github.com/aura-studio/tango/internal/source/file"
	"github.com/aura-studio/tango/internal/source/httpbody"
	"github.com/aura-studio/tango/internal/source/mem"
	"github.com/aura-studio/tango/internal/source/stdin"
	"github.com/aura-studio/tango/internal/source/tailer"
)

// Source streams log lines onto a channel. Run returns a receive-only channel
// that the caller drains; the implementation closes it when the source is
// exhausted (finite sources such as httpbody) or when ctx is cancelled
// (long-running sources such as tailer).
type Source interface {
	Run(ctx context.Context) <-chan string
}

// ---------------------------------------------------------------------------
// subpackage facade
//
// The source package fronts its subpackages with constructors so external
// layers (the role services and the process tests) build a Source through the
// source package alone, never importing source/httpbody, source/stdin or
// source/tailer directly — mirroring how dao fronts its store and parser fronts
// talog/filter.
// ---------------------------------------------------------------------------

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

// FileConfig is the one-shot file-import source's configuration
// (source.file.*), re-exported so the role layer names it through the source
// facade alone (the DAO-6 boundary: role/* never imports source subpackages).
// Alias for file.Config.
type FileConfig = file.Config

// NewFile returns the finite one-shot file-import Source configured by cfg
// (source.file.*): it reads each explicitly-listed file from start to EOF, then
// closes the channel. No glob, no directories, no tailer dependency. Used by the
// file faces (cli function=file and Engine.File) to bulk import already-on-disk
// log files.
func NewFile(cfg *FileConfig) Source {
	if cfg == nil {
		cfg = &FileConfig{}
	}
	return file.New(cfg.Paths, cfg.MaxLineBytes)
}

// NewMem returns an in-memory relay Source: a single producer pushes already-
// formed TA log lines into it via Push and ends the stream with Close, while
// the process pipeline drains them concurrently. buf bounds how far the
// producer may run ahead (Push blocks when full). It is the in-memory analogue
// of NewFile and is used by the backfill domain to feed fetched ThinkingData
// rows through the normal upload pipeline without its own write path. The
// concrete *mem.Source is returned (not the Source interface) so the caller can
// Push/Close; it also satisfies Source for the uploader.
func NewMem(buf int) *mem.Source { return mem.New(buf) }
