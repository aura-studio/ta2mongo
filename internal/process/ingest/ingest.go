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
// are shared with the pipeline via internal/process/ingestion.Processor; this
// package only differs in lifecycle (synchronous, immediate writes).
package ingest

import (
	"context"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/filter"
	"rocket-nano/tools/tango/internal/core/runtime"
	"rocket-nano/tools/tango/internal/core/store"
	"rocket-nano/tools/tango/internal/core/talog"
	"rocket-nano/tools/tango/internal/process/ingestion"
)

// Ingester processes individual JSON log lines synchronously.
// It is safe for concurrent use from multiple goroutines.
type Ingester struct {
	proc   *ingestion.Processor
	store  *store.Store
	mongo  *runtime.MongoResource
	logger *logrus.Logger
}

// ErrFiltered is returned by Ingest when the line is dropped by user-defined
// filter rules. Callers can distinguish intentional drops from real errors.
var ErrFiltered = errors.New("ingest: dropped by filter")

// New connects to MongoDB and creates a ready-to-use Ingester.
// The caller must call Close when the Ingester is no longer needed.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Ingester, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	res, err := runtime.ConnectMongo(ctx, cfg.Mongo)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	return newIngester(res, flt, cfg, logger), nil
}

// NewFromClient creates an Ingester from an existing MongoDB client, avoiding a
// second connection. The client is borrowed: Close will not disconnect it (the
// caller owns its lifecycle). Returns an error if the configured filter
// expressions fail to compile.
func NewFromClient(client *mongo.Client, cfg config.Config, logger *logrus.Logger) (*Ingester, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	res, err := runtime.Borrow(client, cfg.Mongo.URI)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	return newIngester(res, flt, cfg, logger), nil
}

func newIngester(res *runtime.MongoResource, flt *filter.Filter, cfg config.Config, logger *logrus.Logger) *Ingester {
	st := runtime.NewStore(res.DB, cfg, logger)
	return &Ingester{
		proc:   ingestion.NewProcessor(talog.NewParser(), filter.NewHolder(flt), st, ingestion.NoopStats{}, ingestion.WriteOptions{}),
		store:  st,
		mongo:  res,
		logger: logger,
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
	return ig.store.EnsureIndexes(ctx)
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
	case ingestion.KindParseError:
		ig.writeDeadLetter(ctx, res.Model)
		return fmt.Errorf("ingest: parse: %w", res.Err)
	case ingestion.KindFiltered:
		return ErrFiltered
	case ingestion.KindIdentityError:
		ig.writeDeadLetter(ctx, res.Model)
		return fmt.Errorf("ingest: identity resolve: %w", res.Err)
	case ingestion.KindUser:
		if err := ig.store.BulkWriteOrdered(ctx, ig.store.UserCollection(), []mongo.WriteModel{res.Model}); err != nil {
			return fmt.Errorf("ingest: write user: %w", err)
		}
	case ingestion.KindEvent:
		if err := ig.store.BulkWrite(ctx, ig.store.EventCollection(), []mongo.WriteModel{res.Model}); err != nil {
			return fmt.Errorf("ingest: write event: %w", err)
		}
	}
	return nil
}

func (ig *Ingester) writeDeadLetter(ctx context.Context, model mongo.WriteModel) {
	if err := ig.store.BulkWrite(ctx, ig.store.DeadLetterCollection(), []mongo.WriteModel{model}); err != nil {
		ig.logger.WithError(err).Warn("ingest: failed to write dead letter")
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
		userModels  []mongo.WriteModel
		eventModels []mongo.WriteModel
		deadModels  []mongo.WriteModel
	)

	for _, line := range lines {
		res := ig.proc.Process(ctx, line)
		switch res.Kind {
		case ingestion.KindParseError:
			ig.logger.WithError(res.Err).Debug("ingest batch: invalid line")
			deadModels = append(deadModels, res.Model)
		case ingestion.KindIdentityError:
			ig.logger.WithError(res.Err).Warn("ingest batch: identity resolve failed")
			deadModels = append(deadModels, res.Model)
		case ingestion.KindFiltered:
			// intentionally discarded
		case ingestion.KindUser:
			userModels = append(userModels, res.Model)
		case ingestion.KindEvent:
			eventModels = append(eventModels, res.Model)
		}
	}

	// Flush all batches. User collection uses ordered writes for correctness.
	var firstErr error
	if len(userModels) > 0 {
		if err := ig.store.BulkWriteOrdered(ctx, ig.store.UserCollection(), userModels); err != nil {
			firstErr = fmt.Errorf("ingest batch: write user: %w", err)
		}
	}
	if len(eventModels) > 0 {
		if err := ig.store.BulkWrite(ctx, ig.store.EventCollection(), eventModels); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ingest batch: write event: %w", err)
		}
	}
	if len(deadModels) > 0 {
		if err := ig.store.BulkWrite(ctx, ig.store.DeadLetterCollection(), deadModels); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ingest batch: write dead letter: %w", err)
		}
	}

	return firstErr
}
