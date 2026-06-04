package single

import (
	"context"
	"sync"

	drivermongo "go.mongodb.org/mongo-driver/mongo"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process/core"
	"github.com/aura-studio/tango/internal/source"
)

// Uploader is the "single" upload strategy: it consumes a source and writes
// each line to MongoDB immediately (one bulk write per line), blocking until
// the source is drained or the context is cancelled.
//
// "Single" denotes a single per-line attempt — not single-line input. Like the
// other strategies it consumes a source carrying an array of log lines; it just
// commits each line on its own rather than accumulating a batch.
type Uploader struct {
	proc  *core.Processor
	store *dao.Store
	stats core.StatsCollector

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewUploader builds a single-mode Uploader. prs carries the parser and its
// filter (a parser built with a nil filter keeps every record); a nil stats
// collector is treated as a no-op.
func NewUploader(st *dao.Store, prs *parser.Parser, stats core.StatsCollector) *Uploader {
	if stats == nil {
		stats = core.NoopStats{}
	}
	return &Uploader{
		proc:  core.NewProcessor(prs, st, stats),
		store: st,
		stats: stats,
	}
}

// Run consumes lines from src, writing each immediately, and blocks until src
// is drained (returns nil) or ctx is cancelled (returns ctx.Err()). Per-line
// parse/identity failures are routed to dead_letter and recorded in stats; they
// do not stop the run.
func (u *Uploader) Run(ctx context.Context, src source.Source) error {
	ctx, cancel := context.WithCancel(ctx)
	u.setCancel(cancel)
	defer cancel()

	lineCh := src.Run(ctx)
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				return nil
			}
			u.write(ctx, u.proc.Process(ctx, line))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Stop signals a running Run to stop early. It is safe to call before Run and
// to call multiple times.
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

// write commits one classified Result to MongoDB. User writes use ordered bulk
// writes to preserve per-user operation sequence.
func (u *Uploader) write(ctx context.Context, res core.Result) {
	switch res.Kind {
	case core.KindParseError, core.KindIdentityError:
		u.writeDeadLetter(ctx, res.Model)
	case core.KindFiltered:
		// intentionally discarded
	case core.KindUser:
		if err := u.store.BulkWriteOrdered(ctx, u.store.UserCollection(), []drivermongo.WriteModel{res.Model}); err != nil {
			u.stats.OnWriteError()
			logging.WithError(err).Error("single: write user failed")
		}
	case core.KindEvent:
		if err := u.store.BulkWrite(ctx, u.store.EventCollection(), []drivermongo.WriteModel{res.Model}); err != nil {
			u.stats.OnWriteError()
			logging.WithError(err).Error("single: write event failed")
		}
	}
}

func (u *Uploader) writeDeadLetter(ctx context.Context, model drivermongo.WriteModel) {
	if err := u.store.BulkWrite(ctx, u.store.DeadLetterCollection(), []drivermongo.WriteModel{model}); err != nil {
		u.stats.OnWriteError()
		logging.WithError(err).Warn("single: failed to write dead letter")
	}
}
