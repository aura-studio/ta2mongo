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
// are shared with the pipeline via internal/process/single.Processor; this
// package only differs in lifecycle (synchronous, immediate writes).
package batch

import (
	"context"
	"errors"
	"fmt"

	drivermongo "go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process/single"
)

// Ingester processes individual JSON log lines synchronously.
// It is safe for concurrent use from multiple goroutines.
type Ingester struct {
	proc *single.Processor
	dao  *dao.Dao
}

// Dao returns the underlying Dao (exposed for callers that need access to the
// underlying store / mongo resource, e.g. the client SDK).
func (ig *Ingester) Dao() *dao.Dao { return ig.dao }

// ErrFiltered is returned by Ingest when the line is dropped by user-defined
// filter rules. Callers can distinguish intentional drops from real errors.
var ErrFiltered = errors.New("ingest: dropped by filter")

// New connects to MongoDB and creates a ready-to-use Ingester.
// The caller must call Close when the Ingester is no longer needed.
func New(ctx context.Context, daoCfg *dao.Config, parserCfg *parser.Config) (*Ingester, error) {
	if parserCfg == nil {
		parserCfg = &parser.Config{}
	}
	src, err := parserCfg.Build()
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	da, err := dao.New(ctx, daoCfg)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	return newIngester(da, src), nil
}

func newIngester(da *dao.Dao, src *parser.Parser) *Ingester {
	return &Ingester{
		proc: single.NewProcessor(src.Parser, src.Filter(), da.Store, single.NoopStats{}, single.WriteOptions{}),
		dao:  da,
	}
}

// Close releases the MongoDB connection.
func (ig *Ingester) Close() error {
	return ig.dao.Mongo.Close()
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (ig *Ingester) EnsureIndexes(ctx context.Context) error {
	return ig.dao.Store.EnsureIndexes(ctx)
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
	case single.KindParseError:
		ig.writeDeadLetter(ctx, res.Model)
		return fmt.Errorf("ingest: parse: %w", res.Err)
	case single.KindFiltered:
		return ErrFiltered
	case single.KindIdentityError:
		ig.writeDeadLetter(ctx, res.Model)
		return fmt.Errorf("ingest: identity resolve: %w", res.Err)
	case single.KindUser:
		if err := ig.dao.Store.BulkWriteOrdered(ctx, ig.dao.Store.UserCollection(), []drivermongo.WriteModel{res.Model}); err != nil {
			return fmt.Errorf("ingest: write user: %w", err)
		}
	case single.KindEvent:
		if err := ig.dao.Store.BulkWrite(ctx, ig.dao.Store.EventCollection(), []drivermongo.WriteModel{res.Model}); err != nil {
			return fmt.Errorf("ingest: write event: %w", err)
		}
	}
	return nil
}

func (ig *Ingester) writeDeadLetter(ctx context.Context, model drivermongo.WriteModel) {
	if err := ig.dao.Store.BulkWrite(ctx, ig.dao.Store.DeadLetterCollection(), []drivermongo.WriteModel{model}); err != nil {
		logging.WithError(err).Warn("ingest: failed to write dead letter")
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
		case single.KindParseError:
			logging.WithError(res.Err).Debug("ingest batch: invalid line")
			deadModels = append(deadModels, res.Model)
		case single.KindIdentityError:
			logging.WithError(res.Err).Warn("ingest batch: identity resolve failed")
			deadModels = append(deadModels, res.Model)
		case single.KindFiltered:
			// intentionally discarded
		case single.KindUser:
			userModels = append(userModels, res.Model)
		case single.KindEvent:
			eventModels = append(eventModels, res.Model)
		}
	}

	// Flush all batches. User collection uses ordered writes for correctness.
	var firstErr error
	if len(userModels) > 0 {
		if err := ig.dao.Store.BulkWriteOrdered(ctx, ig.dao.Store.UserCollection(), userModels); err != nil {
			firstErr = fmt.Errorf("ingest batch: write user: %w", err)
		}
	}
	if len(eventModels) > 0 {
		if err := ig.dao.Store.BulkWrite(ctx, ig.dao.Store.EventCollection(), eventModels); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ingest batch: write event: %w", err)
		}
	}
	if len(deadModels) > 0 {
		if err := ig.dao.Store.BulkWrite(ctx, ig.dao.Store.DeadLetterCollection(), deadModels); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ingest batch: write dead letter: %w", err)
		}
	}

	return firstErr
}
