package pipeline

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/dynamicbatch"
	"rocket-nano/tools/tango/internal/core/filter"
	daostore "rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/core/talog"
	"rocket-nano/tools/tango/internal/process/ingestion"
)

// RunWorkers launches N workers with affinity-based dispatch and blocks
// until all workers finish. A nil flt is treated as a no-op filter. flt is a
// Holder so the active filter can be hot-swapped while workers run.
func RunWorkers(ctx context.Context, cfg config.Config, st *daostore.Store,
	parser *talog.Parser, flt *filter.Holder, logger *logrus.Logger,
	lineCh <-chan string, stats ingestion.StatsCollector, opts ingestion.WriteOptions,
) {
	if stats == nil {
		stats = ingestion.NoopStats{}
	}

	workerCount := cfg.Pipeline.BatchWorkers
	chSize := cfg.BatchChannelSize()

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
			worker(ctx, cfg, st, parser, flt, logger, ch, stats, opts)
		}(workerChs[i])
	}

	// Dispatcher goroutine: routes lines to workers by user affinity key.
	go Dispatch(ctx, lineCh, workerChs)

	wg.Wait()
}

// worker processes lines from a channel, batches them, and flushes to MongoDB.
// Per-line parse/filter/identity/route rules live in ingestion.Processor; the
// worker owns only batching, the dynamic flush cadence, and the affinity-local
// dead-letter logging.
func worker(ctx context.Context, cfg config.Config, st *daostore.Store,
	parser *talog.Parser, flt *filter.Holder, logger *logrus.Logger,
	lineCh <-chan string, stats ingestion.StatsCollector, opts ingestion.WriteOptions,
) {
	proc := ingestion.NewProcessor(parser, flt, st, stats, opts)

	userBatch := NewBatch(cfg.BatchSizeMax())
	eventBatch := NewBatch(cfg.BatchSizeMax())
	deadBatch := NewBatch(cfg.Pipeline.DeadLetterCap)

	lastFlush := time.Now()
	flushInterval := cfg.Pipeline.FlushInterval
	invalidCount := 0

	// flush writes accumulated batches to MongoDB and resets them.
	// User batch uses ordered writes to preserve operation sequence within a batch.
	flush := func(flushCtx context.Context) {
		flushBatchOrdered(flushCtx, st, logger, st.UserCollection(), userBatch, stats)
		flushBatch(flushCtx, st, logger, st.EventCollection(), eventBatch, stats)
		flushBatch(flushCtx, st, logger, st.DeadLetterCollection(), deadBatch, stats)
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
			case ingestion.KindParseError:
				invalidCount++
				if invalidCount%1000 == 0 {
					logger.WithError(res.Err).Warnf("dropped invalid line (total invalid=%d)", invalidCount)
				}
				deadBatch.Add(res.Model)
				if deadBatch.Full() || time.Since(lastFlush) >= flushInterval {
					flush(ctx)
				}
				continue
			case ingestion.KindIdentityError:
				logger.WithError(res.Err).Warn("identity resolve failed, sending to dead letter")
				deadBatch.Add(res.Model)
				continue
			case ingestion.KindFiltered:
				continue
			case ingestion.KindUser:
				userBatch.Add(res.Model)
			case ingestion.KindEvent:
				eventBatch.Add(res.Model)
			}

			// Flush on dynamic batch threshold (derived from current backlog)
			// or time-interval triggers.
			backlog := len(lineCh)
			threshold := dynamicbatch.ComputeFlushThreshold(
				cfg.BatchSizeMin(),
				cfg.Pipeline.BatchSize,
				cfg.BatchSizeMax(),
				backlog,
				cfg.BatchChannelSize(),
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
func flushBatch(ctx context.Context, st *daostore.Store, logger *logrus.Logger,
	coll *mongo.Collection, b *Batch, stats ingestion.StatsCollector,
) {
	if b.Empty() {
		return
	}
	if err := st.BulkWrite(ctx, coll, b.Models); err != nil {
		stats.OnWriteError()
		logger.WithError(err).WithField("collection", coll.Name()).
			Error("bulk write failed")
	}
	b.Reset()
}

// flushBatchOrdered writes a batch to the given collection with ordered writes
// to guarantee that operations within the batch are applied sequentially.
func flushBatchOrdered(ctx context.Context, st *daostore.Store, logger *logrus.Logger,
	coll *mongo.Collection, b *Batch, stats ingestion.StatsCollector,
) {
	if b.Empty() {
		return
	}
	if err := st.BulkWriteOrdered(ctx, coll, b.Models); err != nil {
		stats.OnWriteError()
		logger.WithError(err).WithField("collection", coll.Name()).
			Error("bulk write failed")
	}
	b.Reset()
}
