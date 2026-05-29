package pipeline

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/dynamicbatch"
	"rocket-nano/tools/tango/internal/filter"
	"rocket-nano/tools/tango/internal/store"
	"rocket-nano/tools/tango/internal/talog"
)

// StatsCollector is an optional callback interface for recording processing
// statistics. Implementations must be safe for concurrent use.
type StatsCollector interface {
	// OnLine is called for every line received.
	OnLine()
	// OnParseOK is called when a line is successfully parsed.
	OnParseOK()
	// OnParseError is called when a line fails to parse.
	OnParseError()
	// OnIdentityError is called when identity resolution fails.
	OnIdentityError()
	// OnUserWrite is called when a user write model is enqueued.
	OnUserWrite()
	// OnEventWrite is called when an event write model is enqueued.
	OnEventWrite()
	// OnDeadLetter is called when a line is sent to dead letter.
	OnDeadLetter()
	// OnWriteError is called when a bulk write fails.
	OnWriteError()
	// OnFiltered is called when a parsed record is dropped by filter rules.
	OnFiltered()
	// OnFilterError is called when a filter expression evaluation fails.
	OnFilterError()
}

// NoopStats is a StatsCollector that does nothing.
type NoopStats struct{}

func (NoopStats) OnLine()          {}
func (NoopStats) OnParseOK()       {}
func (NoopStats) OnParseError()    {}
func (NoopStats) OnIdentityError() {}
func (NoopStats) OnUserWrite()     {}
func (NoopStats) OnEventWrite()    {}
func (NoopStats) OnDeadLetter()    {}
func (NoopStats) OnWriteError()    {}
func (NoopStats) OnFiltered()      {}
func (NoopStats) OnFilterError()   {}

// WriteOptions tunes write-side behaviour for callers that need to deviate
// from the default per-#type semantics (notably backfill).
type WriteOptions struct {
	// ForceSkipExisting routes every event write through $setOnInsert keyed
	// by #uuid, regardless of the record's #type. Existing documents are
	// never modified — duplicates become no-ops. Recommended for backfill.
	ForceSkipExisting bool
}

// RunWorkers launches N workers with affinity-based dispatch and blocks
// until all workers finish. A nil flt is treated as a no-op filter. flt is a
// Holder so the active filter can be hot-swapped while workers run.
func RunWorkers(ctx context.Context, cfg config.Config, st *store.Store,
	parser *talog.Parser, flt *filter.Holder, logger *logrus.Logger,
	lineCh <-chan string, stats StatsCollector, opts WriteOptions,
) {
	if stats == nil {
		stats = NoopStats{}
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
func worker(ctx context.Context, cfg config.Config, st *store.Store,
	parser *talog.Parser, flt *filter.Holder, logger *logrus.Logger,
	lineCh <-chan string, stats StatsCollector, opts WriteOptions,
) {
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

			stats.OnLine()

			rec, err := parser.ParseLine(line)
			if err != nil {
				invalidCount++
				stats.OnParseError()
				stats.OnDeadLetter()
				if invalidCount%1000 == 0 {
					logger.WithError(err).Warnf("dropped invalid line (total invalid=%d)", invalidCount)
				}
				deadBatch.Add(store.DeadLetterModel(line, err))
				if deadBatch.Full() || time.Since(lastFlush) >= flushInterval {
					flush(ctx)
				}
				continue
			}
			stats.OnParseOK()

			// Apply user-defined include/exclude filter. Dropped records are
			// not written to dead letter — they are intentionally discarded.
			if !flt.Empty() {
				keep, ferr := flt.Keep(rec.Doc)
				if ferr != nil {
					stats.OnFilterError()
					logger.WithError(ferr).Debug("filter: expression evaluation error")
				}
				if !keep {
					stats.OnFiltered()
					continue
				}
			}

			// Resolve user identity for both user and event records.
			userID, err := st.Identity().Resolve(ctx, rec.AccountID, rec.DistinctID)
			if err != nil {
				stats.OnIdentityError()
				stats.OnDeadLetter()
				logger.WithError(err).Warn("identity resolve failed, sending to dead letter")
				deadBatch.Add(store.DeadLetterModel(line, err))
				continue
			}

			// Route the record to the appropriate batch.
			switch rec.Category() {
			case talog.CategoryUser:
				userBatch.Add(store.UserWriteModel(rec.Type, userID, rec.Doc))
				stats.OnUserWrite()
			case talog.CategoryEvent:
				rec.Doc["#user_id"] = userID
				if opts.ForceSkipExisting {
					eventBatch.Add(store.EventWriteModelSkipExisting(rec.UUID, rec.Doc))
				} else {
					eventBatch.Add(store.EventWriteModel(rec.Type, rec.UUID, rec.Doc))
				}
				stats.OnEventWrite()
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
func flushBatch(ctx context.Context, st *store.Store, logger *logrus.Logger,
	coll *mongo.Collection, b *Batch, stats StatsCollector,
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
func flushBatchOrdered(ctx context.Context, st *store.Store, logger *logrus.Logger,
	coll *mongo.Collection, b *Batch, stats StatsCollector,
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
