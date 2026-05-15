// Package daemon orchestrates the ta2mongo pipeline:
// file tailing -> line parsing -> batch accumulation -> MongoDB bulk writes.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/dynamicbatch"
	"rocket-nano/tools/ta2mongo/store"
	"rocket-nano/tools/ta2mongo/tailer"
	"rocket-nano/tools/ta2mongo/talog"
)

// Daemon is the main runtime that connects all components together.
type Daemon struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.Store
	parser *talog.Parser
	client *mongo.Client
}

// New connects to MongoDB and creates a ready-to-run Daemon.
// The caller must call Shutdown after Run returns to disconnect from MongoDB.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Daemon, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Mongo.URI))
	if err != nil {
		return nil, err
	}

	dbName, err := config.MongoDBFromURI(cfg.Mongo.URI)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("daemon: %w", err)
	}
	db := client.Database(dbName)
	st := store.New(db, cfg, logger)
	p := talog.NewParser()

	return &Daemon{cfg: cfg, logger: logger, store: st, parser: p, client: client}, nil
}

// Shutdown disconnects the MongoDB client. It must be called after Run returns
// to ensure all final flushes complete before the connection is closed.
func (d *Daemon) Shutdown() error {
	return d.client.Disconnect(context.Background())
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (d *Daemon) EnsureIndexes(ctx context.Context) error {
	return d.store.EnsureIndexes(ctx)
}

// Run starts the daemon pipeline and blocks until ctx is cancelled.
//
// Flow: tailer -> lineCh -> dispatcher (routes by user affinity) -> workerCh[i] -> worker_i -> MongoDB
//
// The dispatcher extracts #account_id (preferred) or #distinct_id from each line
// and consistently hashes it to a fixed worker. This guarantees that all operations
// for the same user are processed sequentially by a single worker, preventing
// out-of-order overwrites across workers.
func (d *Daemon) Run(ctx context.Context) error {
	if len(d.cfg.TA.LogPattern) == 0 {
		return errors.New("daemon: ta.logPattern is required (at least one regex)")
	}

	// Start the tailer; it returns a channel of log lines.
	t := tailer.New(d.cfg.TA.LogPattern, d.cfg.RescanInterval(), d.logger)
	lineCh := t.Run(ctx)

	workerCount := d.cfg.Batch.WorkerCount
	chSize := d.cfg.Batch.WorkerChSize

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
			d.worker(ctx, ch)
		}(workerChs[i])
	}

	// Dispatcher goroutine: routes lines to workers by user affinity key.
	go d.dispatch(ctx, lineCh, workerChs)

	wg.Wait()
	return nil
}

// dispatch reads lines from the shared tailer channel, extracts a routing key,
// and sends each line to the appropriate worker channel based on consistent hash.
// When lineCh is closed or ctx is cancelled, it closes all worker channels.
func (d *Daemon) dispatch(ctx context.Context, lineCh <-chan string, workerChs []chan string) {
	defer func() {
		for _, ch := range workerChs {
			close(ch)
		}
	}()

	n := len(workerChs)
	for line := range lineCh {
		key := extractRoutingKey(line)
		idx := routeIndex(key, n)
		select {
		case workerChs[idx] <- line:
		case <-ctx.Done():
			return
		}
	}
}

// extractRoutingKey performs a lightweight extraction of the user affinity key
// from a JSON log line. Priority: #account_id > #distinct_id.
// If neither is found (e.g. malformed JSON), returns "" which deterministically
// routes to worker 0.
func extractRoutingKey(line string) string {
	if v := gjson.Get(line, `#account_id`); v.Exists() && v.String() != "" {
		return v.String()
	}
	if v := gjson.Get(line, `#distinct_id`); v.Exists() && v.String() != "" {
		return v.String()
	}
	// Try envelope formats: msg, message, log fields may contain nested JSON.
	for _, envelope := range []string{"msg", "message", "log"} {
		inner := gjson.Get(line, envelope).String()
		if len(inner) < 2 || inner[0] != '{' {
			continue
		}
		if v := gjson.Get(inner, `#account_id`); v.Exists() && v.String() != "" {
			return v.String()
		}
		if v := gjson.Get(inner, `#distinct_id`); v.Exists() && v.String() != "" {
			return v.String()
		}
	}
	return ""
}

// routeIndex returns a consistent worker index for the given key using FNV-1a hash.
func routeIndex(key string, n int) int {
	if key == "" || n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32()) % n
}

// ---------------------------------------------------------------------------
// Worker: parse lines, accumulate batches, flush to MongoDB
// ---------------------------------------------------------------------------

// batch accumulates write models for a single collection.
type batch struct {
	models []mongo.WriteModel
	cap    int
}

func newBatch(capacity int) *batch {
	return &batch{
		models: make([]mongo.WriteModel, 0, capacity),
		cap:    capacity,
	}
}

func (b *batch) add(m mongo.WriteModel) { b.models = append(b.models, m) }
func (b *batch) full() bool             { return len(b.models) >= b.cap }
func (b *batch) empty() bool            { return len(b.models) == 0 }

func (b *batch) reset() {
	b.models = b.models[:0]
}

func (d *Daemon) worker(ctx context.Context, lineCh <-chan string) {
	userBatch := newBatch(d.cfg.Batch.SizeMax)
	eventBatch := newBatch(d.cfg.Batch.SizeMax)
	deadBatch := newBatch(128) // smaller capacity for dead letters

	lastFlush := time.Now()
	flushInterval := d.cfg.FlushInterval()
	invalidCount := 0

	// flush writes accumulated batches to MongoDB and resets them.
	// User batch uses ordered writes to preserve operation sequence within a batch.
	flush := func() {
		d.flushBatchOrdered(ctx, d.store.UserCollection(), userBatch)
		d.flushBatch(ctx, d.store.EventCollection(), eventBatch)
		d.flushBatch(ctx, d.store.DeadLetterCollection(), deadBatch)
		lastFlush = time.Now()
	}

	for line := range lineCh {
		rec, err := d.parser.ParseLine(line)
		if err != nil {
			invalidCount++
			if invalidCount%1000 == 0 {
				d.logger.WithError(err).Warnf("dropped invalid line (total invalid=%d)", invalidCount)
			}
			deadBatch.add(store.DeadLetterModel(line, err))
			if deadBatch.full() || time.Since(lastFlush) >= flushInterval {
				flush()
			}
			continue
		}

		// Resolve user identity for both user and event records.
		userID, err := d.store.Identity().Resolve(ctx, rec.AccountID, rec.DistinctID)
		if err != nil {
			d.logger.WithError(err).Warn("identity resolve failed, sending to dead letter")
			deadBatch.add(store.DeadLetterModel(line, err))
			continue
		}

		// Route the record to the appropriate batch.
		switch rec.Category() {
		case talog.CategoryUser:
			userBatch.add(store.UserWriteModel(rec.Type, userID, rec.Doc))
		case talog.CategoryEvent:
			rec.Doc["#user_id"] = userID
			eventBatch.add(store.EventWriteModel(rec.Type, rec.UUID, rec.Doc))
		}

		// Flush on dynamic batch threshold (derived from current backlog)
		// or time-interval triggers.
		backlog := len(lineCh)
		threshold := dynamicbatch.ComputeFlushThreshold(
			d.cfg.Batch.SizeMin,
			d.cfg.Batch.SizeInitial,
			d.cfg.Batch.SizeMax,
			backlog,
			d.cfg.Batch.WorkerChSize,
		)
		needFlush := len(userBatch.models) >= threshold ||
			len(eventBatch.models) >= threshold ||
			time.Since(lastFlush) >= flushInterval
		if needFlush {
			flush()
		}

		// Check for cancellation without blocking.
		select {
		case <-ctx.Done():
			flush()
			return
		default:
		}
	}

	// Channel closed (tailer stopped). Flush remaining data.
	flush()
}

// flushBatch writes a batch to the given collection (unordered) and resets it.
func (d *Daemon) flushBatch(ctx context.Context, coll *mongo.Collection, b *batch) {
	if b.empty() {
		return
	}
	if err := d.store.BulkWrite(ctx, coll, b.models); err != nil {
		d.logger.WithError(err).WithField("collection", coll.Name()).
			Error("bulk write failed")
	}
	b.reset()
}

// flushBatchOrdered writes a batch to the given collection with ordered writes
// to guarantee that operations within the batch are applied sequentially.
// This prevents intra-batch out-of-order overwrites for the same document.
func (d *Daemon) flushBatchOrdered(ctx context.Context, coll *mongo.Collection, b *batch) {
	if b.empty() {
		return
	}
	if err := d.store.BulkWriteOrdered(ctx, coll, b.models); err != nil {
		d.logger.WithError(err).WithField("collection", coll.Name()).
			Error("bulk write failed")
	}
	b.reset()
}
