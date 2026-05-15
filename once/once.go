// Package once implements a one-shot processing mode that reads all existing
// log files from the beginning, processes them to completion, and exits with
// a detailed summary of statistics including retries, errors, and throughput.
//
// Unlike the daemon mode which tails files incrementally and runs forever,
// the once mode is designed for batch migration, data recovery, or CI/CD
// pipeline scenarios where you want to process all existing data and get
// a clear pass/fail result.
package once

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/dynamicbatch"
	"rocket-nano/tools/ta2mongo/store"
	"rocket-nano/tools/ta2mongo/tailer"
	"rocket-nano/tools/ta2mongo/talog"
)

// Stats holds detailed processing statistics for the once run.
type Stats struct {
	FilesDiscovered int64
	TotalLines      atomic.Int64
	ParsedOK        atomic.Int64
	ParseErrors     atomic.Int64
	IdentityErrors  atomic.Int64
	UserWrites      atomic.Int64
	EventWrites     atomic.Int64
	DeadLetters     atomic.Int64
	WriteErrors     atomic.Int64
	Retries         int64 // populated from store.WriteStats at the end
	StartTime       time.Time
	EndTime         time.Time
}

// Duration returns the total processing duration.
func (s *Stats) Duration() time.Duration {
	return s.EndTime.Sub(s.StartTime)
}

// HasErrors returns true if any errors occurred during processing.
func (s *Stats) HasErrors() bool {
	return s.ParseErrors.Load() > 0 ||
		s.IdentityErrors.Load() > 0 ||
		s.WriteErrors.Load() > 0
}

// Runner is the one-shot processor.
type Runner struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.Store
	parser *talog.Parser
	client *mongo.Client
	stats  Stats
}

// New connects to MongoDB and creates a ready-to-run Runner.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Runner, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Mongo.URI))
	if err != nil {
		return nil, fmt.Errorf("once: connect to mongo: %w", err)
	}

	dbName, err := config.MongoDBFromURI(cfg.Mongo.URI)
	if err != nil {
		return nil, fmt.Errorf("once: %w", err)
	}
	db := client.Database(dbName)
	st := store.New(db, cfg, logger)

	return &Runner{
		cfg:    cfg,
		logger: logger,
		store:  st,
		parser: talog.NewParser(),
		client: client,
	}, nil
}

// Close disconnects the MongoDB client.
func (r *Runner) Close() error {
	return r.client.Disconnect(context.Background())
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (r *Runner) EnsureIndexes(ctx context.Context) error {
	return r.store.EnsureIndexes(ctx)
}

// Run executes the one-shot processing: discovers files, reads them from the
// beginning, processes all lines, and returns the final statistics.
// It returns an error only for fatal infrastructure issues (e.g. no log patterns).
// Individual line failures are captured in Stats, not as a returned error.
func (r *Runner) Run(ctx context.Context) (*Stats, error) {
	if len(r.cfg.TA.LogPattern) == 0 {
		return nil, fmt.Errorf("once: ta.logPattern is required (at least one regex)")
	}

	r.stats.StartTime = time.Now()

	// Discover files using the same logic as the tailer (regex-based).
	files := tailer.DiscoverFiles(r.cfg.TA.LogPattern, r.logger)
	r.stats.FilesDiscovered = int64(len(files))

	if len(files) == 0 {
		r.logger.Warn("once: no files matched ta.logPattern, nothing to process")
		r.stats.EndTime = time.Now()
		return &r.stats, nil
	}

	r.logger.WithField("files", len(files)).Info("once: discovered files to process")

	// Feed all file lines into a channel.
	lineCh := r.readAllFiles(ctx, files)

	// Process lines using the same worker model as daemon but bounded.
	workerCount := r.cfg.Batch.WorkerCount
	chSize := r.cfg.Batch.WorkerChSize

	workerChs := make([]chan string, workerCount)
	for i := range workerChs {
		workerChs[i] = make(chan string, chSize)
	}

	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func(ch <-chan string) {
			defer wg.Done()
			r.worker(ctx, ch)
		}(workerChs[i])
	}

	// Dispatcher: route lines to workers using the same affinity logic.
	go dispatch(ctx, lineCh, workerChs)

	wg.Wait()

	r.stats.Retries = r.store.Stats().TotalRetries()
	r.stats.EndTime = time.Now()

	r.printSummary()

	return &r.stats, nil
}

// readAllFiles reads all discovered files from the beginning (not follow mode)
// and sends every non-empty line to the returned channel.
func (r *Runner) readAllFiles(ctx context.Context, files []string) <-chan string {
	out := make(chan string, 2000)

	go func() {
		defer close(out)
		for _, path := range files {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if err := r.readFile(ctx, path, out); err != nil {
				r.logger.WithError(err).WithField("path", path).Error("once: failed to read file")
			}
		}
	}()

	return out
}

// readFile opens a file and sends all non-empty lines to out.
func (r *Runner) readFile(ctx context.Context, path string, out chan<- string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r.logger.WithField("path", path).Debug("once: reading file from beginning")

	scanner := bufio.NewScanner(f)
	// Support lines up to 10 MB (same as ingest mode).
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if len(line) == 0 {
			continue
		}

		select {
		case out <- line:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return scanner.Err()
}

// dispatch routes lines to workers using FNV-1a affinity hashing (same as daemon).
func dispatch(ctx context.Context, lineCh <-chan string, workerChs []chan string) {
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

// worker processes lines from a channel, batches them, and flushes to MongoDB.
func (r *Runner) worker(ctx context.Context, lineCh <-chan string) {
	userBatch := newBatch(r.cfg.Batch.SizeMax)
	eventBatch := newBatch(r.cfg.Batch.SizeMax)
	deadBatch := newBatch(128)

	lastFlush := time.Now()
	flushInterval := r.cfg.FlushInterval()

	flush := func() {
		r.flushBatchOrdered(ctx, r.store.UserCollection(), userBatch)
		r.flushBatch(ctx, r.store.EventCollection(), eventBatch)
		r.flushBatch(ctx, r.store.DeadLetterCollection(), deadBatch)
		lastFlush = time.Now()
	}

	for line := range lineCh {
		r.stats.TotalLines.Add(1)

		rec, err := r.parser.ParseLine(line)
		if err != nil {
			r.stats.ParseErrors.Add(1)
			r.stats.DeadLetters.Add(1)
			deadBatch.add(store.DeadLetterModel(line, err))
			if deadBatch.full() || time.Since(lastFlush) >= flushInterval {
				flush()
			}
			continue
		}
		r.stats.ParsedOK.Add(1)

		userID, err := r.store.Identity().Resolve(ctx, rec.AccountID, rec.DistinctID)
		if err != nil {
			r.stats.IdentityErrors.Add(1)
			r.stats.DeadLetters.Add(1)
			deadBatch.add(store.DeadLetterModel(line, err))
			continue
		}

		switch rec.Category() {
		case talog.CategoryUser:
			userBatch.add(store.UserWriteModel(rec.Type, userID, rec.Doc))
			r.stats.UserWrites.Add(1)
		case talog.CategoryEvent:
			rec.Doc["#user_id"] = userID
			eventBatch.add(store.EventWriteModel(rec.Type, rec.UUID, rec.Doc))
			r.stats.EventWrites.Add(1)
		}

		// Flush on dynamic batch threshold (derived from current queue backlog)
		// or time-interval triggers.
		backlog := len(lineCh)
		threshold := dynamicbatch.ComputeFlushThreshold(
			r.cfg.Batch.SizeMin,
			r.cfg.Batch.SizeInitial,
			r.cfg.Batch.SizeMax,
			backlog,
			r.cfg.Batch.WorkerChSize,
		)
		needFlush := len(userBatch.models) >= threshold ||
			len(eventBatch.models) >= threshold ||
			time.Since(lastFlush) >= flushInterval
		if needFlush {
			flush()
		}

		select {
		case <-ctx.Done():
			flush()
			return
		default:
		}
	}

	// Channel closed. Flush remaining data.
	flush()
}

// flushBatch writes a batch to the given collection (unordered) and resets it.
func (r *Runner) flushBatch(ctx context.Context, coll *mongo.Collection, b *batch) {
	if b.empty() {
		return
	}
	if err := r.store.BulkWrite(ctx, coll, b.models); err != nil {
		r.stats.WriteErrors.Add(1)
		r.logger.WithError(err).WithField("collection", coll.Name()).
			Error("once: bulk write failed")
	}
	b.reset()
}

// flushBatchOrdered writes a batch with ordered writes and resets it.
func (r *Runner) flushBatchOrdered(ctx context.Context, coll *mongo.Collection, b *batch) {
	if b.empty() {
		return
	}
	if err := r.store.BulkWriteOrdered(ctx, coll, b.models); err != nil {
		r.stats.WriteErrors.Add(1)
		r.logger.WithError(err).WithField("collection", coll.Name()).
			Error("once: bulk write failed")
	}
	b.reset()
}

// printSummary logs a detailed summary of the processing run.
func (r *Runner) printSummary() {
	s := &r.stats
	duration := s.Duration()

	r.logger.Info("========== once mode: processing summary ==========")
	r.logger.WithFields(logrus.Fields{
		"files_discovered": s.FilesDiscovered,
		"total_lines":      s.TotalLines.Load(),
		"duration":         duration.Round(time.Millisecond),
	}).Info("overview")

	r.logger.WithFields(logrus.Fields{
		"parsed_ok":       s.ParsedOK.Load(),
		"parse_errors":    s.ParseErrors.Load(),
		"identity_errors": s.IdentityErrors.Load(),
	}).Info("parsing")

	r.logger.WithFields(logrus.Fields{
		"user_writes":  s.UserWrites.Load(),
		"event_writes": s.EventWrites.Load(),
		"dead_letters": s.DeadLetters.Load(),
	}).Info("writes")

	r.logger.WithFields(logrus.Fields{
		"total_retries": s.Retries,
		"write_errors":  s.WriteErrors.Load(),
	}).Info("reliability")

	if totalLines := s.TotalLines.Load(); totalLines > 0 && duration > 0 {
		lps := float64(totalLines) / duration.Seconds()
		r.logger.WithField("lines_per_second", fmt.Sprintf("%.0f", lps)).Info("throughput")
	}

	if s.HasErrors() {
		r.logger.Warn("========== once mode: COMPLETED WITH ERRORS ==========")
	} else {
		r.logger.Info("========== once mode: COMPLETED SUCCESSFULLY ==========")
	}
}
