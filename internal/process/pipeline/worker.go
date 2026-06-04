package pipeline

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/mongo"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process/core"
)

// RunWorkers launches N workers with affinity-based dispatch and blocks
// until all workers finish. prs carries the parser and its filter holder, whose
// active filter can be hot-swapped while workers run.
func RunWorkers(ctx context.Context, cfg *Config, st *dao.Store,
	prs *parser.Parser,
	lineCh <-chan string, stats core.StatsCollector,
) {
	if stats == nil {
		stats = core.NoopStats{}
	}

	workerCount := cfg.BatchWorkers
	chSize := cfg.ChannelSize()

	// Create per-worker channels for affinity-based routing.
	workerChs := make([]chan string, workerCount)
	for i := range workerChs {
		workerChs[i] = make(chan string, chSize)
	}

	// Launch N workers, each consuming from its own dedicated channel.
	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func(ch <-chan string) {
			defer wg.Done()
			defer logging.Recover("pipeline worker")
			worker(ctx, cfg, st, prs, ch, stats)
		}(workerChs[i])
	}

	// Dispatcher goroutine: routes lines to workers by user affinity key.
	go func() {
		defer logging.Recover("pipeline dispatch")
		Dispatch(ctx, lineCh, workerChs)
	}()

	wg.Wait()
}

// worker processes lines from a channel, batches them, and flushes to MongoDB.
// Per-line parse/filter/identity/route rules live in core.Processor; the
// worker owns only batching, the dynamic flush cadence, and the affinity-local
// dead-letter logging.
func worker(ctx context.Context, cfg *Config, st *dao.Store,
	prs *parser.Parser,
	lineCh <-chan string, stats core.StatsCollector,
) {
	proc := core.NewProcessor(prs, st, stats)

	userBatch := NewBatch(cfg.MaxBatchSize())
	eventBatch := NewBatch(cfg.MaxBatchSize())
	deadBatch := NewBatch(cfg.DeadLetterCap)

	lastFlush := time.Now()
	flushInterval := cfg.FlushInterval
	invalidCount := 0

	// flush writes accumulated batches to MongoDB and resets them.
	// User batch uses ordered writes to preserve operation sequence within a batch.
	flush := func(flushCtx context.Context) {
		flushBatchOrdered(flushCtx, st, st.UserCollection(), userBatch, stats)
		flushBatch(flushCtx, st, st.EventCollection(), eventBatch, stats)
		flushBatch(flushCtx, st, st.DeadLetterCollection(), deadBatch, stats)
		lastFlush = time.Now()
	}

	// Use a ticker to ensure batches are flushed even when no new lines arrive.
	flushTicker := time.NewTicker(flushInterval)
	defer flushTicker.Stop()

	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				// Channel closed (tailer stopped). Flush remaining data.
				flush(context.Background())
				return
			}

			res := proc.Process(ctx, line)
			switch res.Kind {
			case core.KindParseError:
				invalidCount++
				if invalidCount%1000 == 0 {
					logging.WithError(res.Err).Warnf("dropped invalid line (total invalid=%d)", invalidCount)
				}
				deadBatch.Add(res.Model)
				if deadBatch.Full() || time.Since(lastFlush) >= flushInterval {
					flush(ctx)
				}
				continue
			case core.KindIdentityError:
				logging.WithError(res.Err).Warn("identity resolve failed, sending to dead letter")
				deadBatch.Add(res.Model)
				continue
			case core.KindFiltered:
				continue
			case core.KindUser:
				userBatch.Add(res.Model)
			case core.KindEvent:
				eventBatch.Add(res.Model)
			}

			// Flush on dynamic batch threshold (derived from current backlog)
			// or time-interval triggers.
			backlog := len(lineCh)
			threshold := ComputeFlushThreshold(
				cfg.MinBatchSize(),
				cfg.BatchSize,
				cfg.MaxBatchSize(),
				backlog,
				cfg.ChannelSize(),
			)
			needFlush := userBatch.Len() >= threshold ||
				eventBatch.Len() >= threshold ||
				time.Since(lastFlush) >= flushInterval
			if needFlush {
				flush(ctx)
			}

		case <-flushTicker.C:
			// Periodic flush to ensure data is written even when idle.
			if !userBatch.Empty() || !eventBatch.Empty() || !deadBatch.Empty() {
				flush(ctx)
			}

		case <-ctx.Done():
			// Use a background context for the final flush so remaining
			// data is written even after the main context is cancelled.
			flush(context.Background())
			return
		}
	}
}

// flushBatch writes a batch to the given collection (unordered) and resets it.
func flushBatch(ctx context.Context, st *dao.Store,
	coll *mongo.Collection, b *Batch, stats core.StatsCollector,
) {
	if b.Empty() {
		return
	}
	if err := st.BulkWrite(ctx, coll, b.Models); err != nil {
		stats.OnWriteError()
		logging.WithError(err).WithField("collection", coll.Name()).
			Error("bulk write failed")
	}
	b.Reset()
}

// flushBatchOrdered writes a batch to the given collection with ordered writes
// to guarantee that operations within the batch are applied sequentially.
func flushBatchOrdered(ctx context.Context, st *dao.Store,
	coll *mongo.Collection, b *Batch, stats core.StatsCollector,
) {
	if b.Empty() {
		return
	}
	if err := st.BulkWriteOrdered(ctx, coll, b.Models); err != nil {
		stats.OnWriteError()
		logging.WithError(err).WithField("collection", coll.Name()).
			Error("bulk write failed")
	}
	b.Reset()
}
