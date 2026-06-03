// Package ingest provides a synchronous, blocking API for processing
// individual ThinkingData JSON log lines.
//
// Unlike the async pipeline (which tails log files, batches records, and writes
// them via background workers), the ingest package processes one line at a time
// and blocks until the MongoDB write completes.
//
// This is designed for request-response scenarios such as HTTP API handlers,
// CLI one-shot imports, or SDK integrations where the caller needs to know
// immediately whether the write succeeded.
//
// The per-line rules (parse → filter → identity → write model / dead letter)
// are shared with the pipeline via internal/process/processor.Processor; this
// package only differs in lifecycle (synchronous, immediate writes).
package ingest

import (
	"context"
	"errors"
	"fmt"

	drivermongo "go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/dao"
	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/log"
	"rocket-nano/tools/tango/internal/process/processor"
	"rocket-nano/tools/tango/internal/source"
)

// Ingester processes individual JSON log lines synchronously.
// It is safe for concurrent use from multiple goroutines.
type Ingester struct {
	proc  *processor.Processor
	dao   *dao.Dao
	mongo *daomongo.MongoResource
}

// ErrFiltered is returned by Ingest when the line is dropped by user-defined
// filter rules. Callers can distinguish intentional drops from real errors.
var ErrFiltered = errors.New("ingest: dropped by filter")

// New connects to MongoDB and creates a ready-to-use Ingester.
// The caller must call Close when the Ingester is no longer needed.
func New(ctx context.Context, cfg config.Config) (*Ingester, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	res, err := daomongo.ConnectMongo(ctx, cfg.Mongo)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	return newIngester(res, source.New(flt), cfg), nil
}

// NewFromClient creates an Ingester from an existing MongoDB client, avoiding a
// second connection. The client is borrowed: Close will not disconnect it (the
// caller owns its lifecycle). Returns an error if the configured filter
// expressions fail to compile.
func NewFromClient(client *drivermongo.Client, cfg config.Config) (*Ingester, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	res, err := daomongo.Borrow(client, cfg.Mongo.URI)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	return newIngester(res, source.New(flt), cfg), nil
}

func newIngester(res *daomongo.MongoResource, src *source.Source, cfg config.Config) *Ingester {
	da := dao.New(res.DB, cfg)
	return &Ingester{
		proc:  processor.NewProcessor(src.Parser, src.Filter(), da.Store, processor.NoopStats{}, processor.WriteOptions{}),
		dao:   da,
		mongo: res,
	}
}

// Close releases the MongoDB connection when the Ingester owns it (created with
// New). For an Ingester created with NewFromClient the connection is borrowed
// and Close is a no-op — the caller owns the client's lifecycle.
func (ig *Ingester) Close() error {
	return ig.mongo.Close()
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (ig *Ingester) EnsureIndexes(ctx context.Context) error {
	return ig.dao.EnsureIndexes(ctx)
}

// Ingest parses a single JSON log line, resolves user identity, and writes the
// result to MongoDB. It blocks until the write is confirmed or fails.
//
// On parse failure the line is written to dead_letter and the parse error is
// returned. On filter drop it returns ErrFiltered. This method is safe for
// concurrent use.
func (ig *Ingester) Ingest(ctx context.Context, line string) error {
	res := ig.proc.Process(ctx, line)
	switch res.Kind {
	case processor.KindParseError:
		ig.writeDeadLetter(ctx, res.Model)
		return fmt.Errorf("ingest: parse: %w", res.Err)
	case processor.KindFiltered:
		return ErrFiltered
	case processor.KindIdentityError:
		ig.writeDeadLetter(ctx, res.Model)
		return fmt.Errorf("ingest: identity resolve: %w", res.Err)
	case processor.KindUser:
		if err := ig.dao.BulkWriteOrdered(ctx, ig.dao.UserCollection(), []drivermongo.WriteModel{res.Model}); err != nil {
			return fmt.Errorf("ingest: write user: %w", err)
		}
	case processor.KindEvent:
		if err := ig.dao.BulkWrite(ctx, ig.dao.EventCollection(), []drivermongo.WriteModel{res.Model}); err != nil {
			return fmt.Errorf("ingest: write event: %w", err)
		}
	}
	return nil
}

func (ig *Ingester) writeDeadLetter(ctx context.Context, model drivermongo.WriteModel) {
	if err := ig.dao.BulkWrite(ctx, ig.dao.DeadLetterCollection(), []drivermongo.WriteModel{model}); err != nil {
		log.WithError(err).Warn("ingest: failed to write dead letter")
	}
}

// IngestBatch parses and writes multiple JSON log lines in a single batch. It
// processes all lines, collecting write models, then flushes them together.
// Lines that fail to parse or resolve identity are sent to dead_letter.
//
// Returns an error if any batch write to MongoDB fails. Per-line parse/identity
// errors are logged but do not prevent other lines from being processed.
func (ig *Ingester) IngestBatch(ctx context.Context, lines []string) error {
	var (
		userModels  []drivermongo.WriteModel
		eventModels []drivermongo.WriteModel
		deadModels  []drivermongo.WriteModel
	)

	for _, line := range lines {
		res := ig.proc.Process(ctx, line)
		switch res.Kind {
		case processor.KindParseError:
			log.WithError(res.Err).Debug("ingest batch: invalid line")
			deadModels = append(deadModels, res.Model)
		case processor.KindIdentityError:
			log.WithError(res.Err).Warn("ingest batch: identity resolve failed")
			deadModels = append(deadModels, res.Model)
		case processor.KindFiltered:
			// intentionally discarded
		case processor.KindUser:
			userModels = append(userModels, res.Model)
		case processor.KindEvent:
			eventModels = append(eventModels, res.Model)
		}
	}

	// Flush all batches. User collection uses ordered writes for correctness.
	var firstErr error
	if len(userModels) > 0 {
		if err := ig.dao.BulkWriteOrdered(ctx, ig.dao.UserCollection(), userModels); err != nil {
			firstErr = fmt.Errorf("ingest batch: write user: %w", err)
		}
	}
	if len(eventModels) > 0 {
		if err := ig.dao.BulkWrite(ctx, ig.dao.EventCollection(), eventModels); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ingest batch: write event: %w", err)
		}
	}
	if len(deadModels) > 0 {
		if err := ig.dao.BulkWrite(ctx, ig.dao.DeadLetterCollection(), deadModels); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ingest batch: write dead letter: %w", err)
		}
	}

	return firstErr
}
