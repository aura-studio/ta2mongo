// Package batch provides the synchronous, batched upload strategy: it drains a
// source, accumulates write models, and flushes them to MongoDB in bulk writes
// of at most BatchSize records.
//
// Unlike the async pipeline (N background workers with affinity routing), batch
// processing is single-goroutine and flushes deterministically on the batch-size
// threshold and at end-of-source. The per-line rules (parse -> filter ->
// identity -> write model / dead letter) are shared with the other strategies
// via internal/process/core.Processor; this package only owns batching and the
// bulk flush.
package batch

import (
	"context"
	"fmt"
	"sync"

	drivermongo "go.mongodb.org/mongo-driver/mongo"

	daostore "rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/parser/talog"
	"rocket-nano/tools/tango/internal/process/core"
	"rocket-nano/tools/tango/internal/source"
)

// DefaultBatchSize is the bulk-write flush size used when none is configured.
const DefaultBatchSize = 1000

// Uploader is the "batch" upload strategy: drain the source, accumulate write
// models, and flush them in bulk writes of at most batchSize records.
type Uploader struct {
	proc      *core.Processor
	store     *daostore.Store
	stats     core.StatsCollector
	batchSize int

	mu     sync.Mutex
	cancel context.CancelFunc
}

// NewUploader builds a batch-mode Uploader. A non-positive batchSize falls back
// to DefaultBatchSize; a nil filter holder keeps every record; a nil stats
// collector is treated as a no-op.
func NewUploader(st *daostore.Store, parser *talog.Parser, flt *filter.Holder, batchSize int, stats core.StatsCollector, opts core.WriteOptions) *Uploader {
	if batchSize <= 0 {
		batchSize = DefaultBatchSize
	}
	if stats == nil {
		stats = core.NoopStats{}
	}
	return &Uploader{
		proc:      core.NewProcessor(parser, flt, st, stats, opts),
		store:     st,
		stats:     stats,
		batchSize: batchSize,
	}
}

// Run consumes lines from src, accumulating up to batchSize write models before
// each bulk flush, and blocks until src is drained or ctx is cancelled. It
// always flushes the remaining partial batch before returning. Per-line parse /
// identity failures are routed to dead_letter; they do not stop the run.
//
// It returns the first bulk-write error encountered (or ctx.Err() on
// cancellation when no write error occurred).
func (u *Uploader) Run(ctx context.Context, src source.Source) error {
	ctx, cancel := context.WithCancel(ctx)
	u.setCancel(cancel)
	defer cancel()

	var (
		userModels  []drivermongo.WriteModel
		eventModels []drivermongo.WriteModel
		deadModels  []drivermongo.WriteModel
		pending     int
		firstErr    error
	)

	flush := func() {
		if err := u.flush(ctx, userModels, eventModels, deadModels); err != nil && firstErr == nil {
			firstErr = err
		}
		userModels, eventModels, deadModels, pending = nil, nil, nil, 0
	}

	lineCh := src.Run(ctx)
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				flush()
				return firstErr
			}
			res := u.proc.Process(ctx, line)
			switch res.Kind {
			case core.KindParseError, core.KindIdentityError:
				deadModels = append(deadModels, res.Model)
			case core.KindFiltered:
				continue
			case core.KindUser:
				userModels = append(userModels, res.Model)
			case core.KindEvent:
				eventModels = append(eventModels, res.Model)
			}
			if pending++; pending >= u.batchSize {
				flush()
			}
		case <-ctx.Done():
			flush()
			if firstErr != nil {
				return firstErr
			}
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

// flush writes the accumulated batches. The user collection uses ordered writes
// to preserve per-user operation sequence within the batch; events and dead
// letters use unordered writes. It returns the first error encountered.
func (u *Uploader) flush(ctx context.Context, userModels, eventModels, deadModels []drivermongo.WriteModel) error {
	var firstErr error
	if len(userModels) > 0 {
		if err := u.store.BulkWriteOrdered(ctx, u.store.UserCollection(), userModels); err != nil {
			u.stats.OnWriteError()
			firstErr = fmt.Errorf("batch: write user: %w", err)
		}
	}
	if len(eventModels) > 0 {
		if err := u.store.BulkWrite(ctx, u.store.EventCollection(), eventModels); err != nil {
			u.stats.OnWriteError()
			if firstErr == nil {
				firstErr = fmt.Errorf("batch: write event: %w", err)
			}
		}
	}
	if len(deadModels) > 0 {
		if err := u.store.BulkWrite(ctx, u.store.DeadLetterCollection(), deadModels); err != nil {
			u.stats.OnWriteError()
			if firstErr == nil {
				firstErr = fmt.Errorf("batch: write dead letter: %w", err)
			}
		}
	}
	return firstErr
}
