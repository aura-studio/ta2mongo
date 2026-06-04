package pipeline

import (
	"context"
	"sync"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process/core"
	"rocket-nano/tools/tango/internal/source"
)

// Uploader is the "pipeline" upload strategy: it streams the source through N
// affinity-routed background workers that batch and flush asynchronously. It is
// the strategy used by the long-running daemon, and also selectable by the
// gateway for high-throughput uploads.
type Uploader struct {
	cfg   *Config
	store *dao.Store
	prs   *parser.Parser
	stats core.StatsCollector
	opts  core.WriteOptions

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewUploader builds a pipeline-mode Uploader. A nil cfg is defaulted; prs
// carries the parser and its filter (a parser built with a nil filter keeps
// every record); a nil stats collector is treated as a no-op.
func NewUploader(cfg *Config, st *dao.Store, prs *parser.Parser, stats core.StatsCollector, opts core.WriteOptions) *Uploader {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.ApplyDefaults()
	if stats == nil {
		stats = core.NoopStats{}
	}
	return &Uploader{cfg: cfg, store: st, prs: prs, stats: stats, opts: opts}
}

// Run streams lines from src through the worker pool and blocks until src is
// drained (finite sources) or ctx is cancelled (long-running sources), after
// which all workers flush their pending batches and exit. It always returns nil
// — write failures are surfaced through stats (OnWriteError), not the return
// value.
func (u *Uploader) Run(ctx context.Context, src source.Source) error {
	ctx, cancel := context.WithCancel(ctx)
	u.setCancel(cancel)
	defer cancel()

	lineCh := src.Run(ctx)
	RunWorkers(ctx, u.cfg, u.store, u.prs, lineCh, u.stats, u.opts)
	return nil
}

// Stop signals the workers to stop early (graceful: they flush before exiting).
// It is safe to call before Run and to call multiple times.
func (u *Uploader) Stop() {
	u.mu.Lock()
	cancel := u.cancel
	u.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (u *Uploader) setCancel(cancel context.CancelFunc) {
	u.mu.Lock()
	u.cancel = cancel
	u.mu.Unlock()
}
