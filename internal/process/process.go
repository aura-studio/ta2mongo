// Package process is the unified entry point to the three upload strategies:
//
//   - single   — per-line immediate writes (internal/process/single)
//   - batch    — synchronous, batched bulk writes (internal/process/batch)
//   - pipeline — asynchronous streaming workers (internal/process/pipeline)
//
// All three implement the same Uploader interface: they consume a Source (a
// stream of log lines) and can be started (Run, blocking) and stopped (Stop).
// External layers (the role services and the client SDK) depend on this package
// only; the single/batch/pipeline sub-packages are managed here and are not
// meant to be imported directly from outside process.
package process

import (
	"context"
	"fmt"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process/batch"
	"github.com/aura-studio/tango/internal/process/core"
	"github.com/aura-studio/tango/internal/process/pipeline"
	"github.com/aura-studio/tango/internal/process/single"
	"github.com/aura-studio/tango/internal/source"
)

// Counters accumulates per-line processing statistics shared by all three
// strategies.
type Counters = core.Counters

// Snapshot is an immutable read of a Counters at a point in time.
type Snapshot = core.Snapshot

// Source is the line-source contract the uploaders consume. source/httpbody and
// source/tailer satisfy it.
type Source = source.Source

// Mode selects one of the three upload strategies.
type Mode string

const (
	// ModeSingle writes each line immediately (one bulk write per line).
	ModeSingle Mode = "single"
	// ModeBatch accumulates write models and flushes them in bulk batches.
	ModeBatch Mode = "batch"
	// ModePipeline streams lines through N async affinity-routed workers.
	ModePipeline Mode = "pipeline"
)

// ParseMode validates s and returns the corresponding Mode.
func ParseMode(s string) (Mode, error) {
	m := Mode(s)
	switch m {
	case ModeSingle, ModeBatch, ModePipeline:
		return m, nil
	default:
		return "", fmt.Errorf("process: unknown upload mode %q (want single|batch|pipeline)", s)
	}
}

// Uploader is the common interface implemented by all three strategies: a
// startable/stoppable consumer of a Source.
type Uploader interface {
	// Run consumes lines from src and processes them, blocking until src is
	// drained (finite sources) or ctx is cancelled / Stop is called
	// (long-running sources), then flushes pending writes.
	Run(ctx context.Context, src Source) error
	// Stop signals a running Run to stop early. It is safe to call before Run
	// and to call multiple times.
	Stop()
}

// New builds the Uploader for cfg.Mode, wiring it to the already-connected dao
// and the shared parser. A nil cfg is defaulted; a nil stats collector is
// treated as a no-op. Connection lifecycle (dao.New / Close) is owned by the
// caller, not by the uploader.
func New(cfg *Config, d *dao.Dao, p *parser.Parser, stats *Counters) (Uploader, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.ApplyDefaults()
	mode, err := cfg.ModeValue()
	if err != nil {
		return nil, err
	}

	var sc core.StatsCollector = core.NoopStats{}
	if stats != nil {
		sc = stats
	}

	switch mode {
	case ModeSingle:
		return single.NewUploader(d.Store, p, sc), nil
	case ModeBatch:
		return batch.NewUploader(d.Store, p, cfg.BatchSize, sc), nil
	case ModePipeline:
		return pipeline.NewUploader(cfg.Pipeline, d.Store, p, sc), nil
	default:
		return nil, fmt.Errorf("process: unknown upload mode %q", mode)
	}
}
