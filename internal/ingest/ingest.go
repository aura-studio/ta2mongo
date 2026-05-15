// Package ingest provides a synchronous, blocking API for processing
// individual ThinkingData JSON log lines.
//
// Unlike the daemon package which tails log files, batches records, and
// writes them asynchronously via background workers, the ingest package
// processes one line at a time and blocks until the MongoDB write completes.
//
// This is designed for request-response scenarios such as HTTP API handlers,
// CLI one-shot imports, or SDK integrations where the caller needs to know
// immediately whether the write succeeded.
//
// Both modes (daemon and ingest) coexist and share the same underlying
// components: talog.Parser, store.Store, and store.IdentityResolver.
package ingest

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/store"
	"rocket-nano/tools/tango/internal/talog"
)

// Ingester processes individual JSON log lines synchronously.
// It is safe for concurrent use from multiple goroutines.
type Ingester struct {
	store  *store.Store
	parser *talog.Parser
	client *mongo.Client
	logger *logrus.Logger
}

// New connects to MongoDB and creates a ready-to-use Ingester.
// The caller must call Close when the Ingester is no longer needed.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Ingester, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, fmt.Errorf("ingest: connect to mongo: %w", err)
	}

	dbName, err := config.MongoDBFromURI(cfg.MongoURI)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	db := client.Database(dbName)
	st := store.New(db, cfg, logger)

	return &Ingester{
		store:  st,
		parser: talog.NewParser(),
		client: client,
		logger: logger,
	}, nil
}

// NewFromClient creates an Ingester from an existing MongoDB client, avoiding
// a second connection. This is useful when the caller already manages the
// MongoDB lifecycle (e.g., when both daemon and ingest modes run in the same
// process).
func NewFromClient(client *mongo.Client, cfg config.Config, logger *logrus.Logger) *Ingester {
	dbName, err := config.MongoDBFromURI(cfg.MongoURI)
	if err != nil {
		// NewFromClient signature doesn't return error; fall back to using URI as-is.
		// This should never happen if cfg.MongoURI is valid.
		dbName = ""
	}
	db := client.Database(dbName)
	st := store.New(db, cfg, logger)

	return &Ingester{
		store:  st,
		parser: talog.NewParser(),
		client: client,
		logger: logger,
	}
}

// Close disconnects the MongoDB client. Only call this if the Ingester was
// created with New (owns the connection). Do NOT call Close if the Ingester
// was created with NewFromClient (the caller owns the connection).
func (ig *Ingester) Close() error {
	return ig.client.Disconnect(context.Background())
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (ig *Ingester) EnsureIndexes(ctx context.Context) error {
	return ig.store.EnsureIndexes(ctx)
}

// Ingest parses a single JSON log line, resolves user identity, and writes
// the result to MongoDB. It blocks until the write is confirmed or fails.
//
// The line must be a valid ThinkingData JSON payload (direct or envelope format).
// On parse failure the line is written to the dead_letter collection and
// the parse error is returned.
//
// This method is safe for concurrent use.
func (ig *Ingester) Ingest(ctx context.Context, line string) error {
	rec, err := ig.parser.ParseLine(line)
	if err != nil {
		// Write to dead letter so the failed line is persisted for inspection,
		// then return the parse error to the caller.
		dlModel := store.DeadLetterModel(line, err)
		if wErr := ig.store.BulkWrite(ctx, ig.store.DeadLetterCollection(), []mongo.WriteModel{dlModel}); wErr != nil {
			ig.logger.WithError(wErr).Warn("ingest: failed to write dead letter")
		}
		return fmt.Errorf("ingest: parse: %w", err)
	}

	// Resolve user identity.
	userID, err := ig.store.Identity().Resolve(ctx, rec.AccountID, rec.DistinctID)
	if err != nil {
		// Identity resolution failure is also persisted to dead letter.
		dlModel := store.DeadLetterModel(line, err)
		if wErr := ig.store.BulkWrite(ctx, ig.store.DeadLetterCollection(), []mongo.WriteModel{dlModel}); wErr != nil {
			ig.logger.WithError(wErr).Warn("ingest: failed to write dead letter")
		}
		return fmt.Errorf("ingest: identity resolve: %w", err)
	}

	// Route to the appropriate collection and write.
	switch rec.Category() {
	case talog.CategoryUser:
		model := store.UserWriteModel(rec.Type, userID, rec.Doc)
		if err := ig.store.BulkWriteOrdered(ctx, ig.store.UserCollection(), []mongo.WriteModel{model}); err != nil {
			return fmt.Errorf("ingest: write user: %w", err)
		}

	case talog.CategoryEvent:
		rec.Doc["#user_id"] = userID
		model := store.EventWriteModel(rec.Type, rec.UUID, rec.Doc)
		if err := ig.store.BulkWrite(ctx, ig.store.EventCollection(), []mongo.WriteModel{model}); err != nil {
			return fmt.Errorf("ingest: write event: %w", err)
		}
	}

	return nil
}

// IngestBatch parses and writes multiple JSON log lines in a single batch.
// It processes all lines, collecting write models, then flushes them together.
// Lines that fail to parse are sent to the dead_letter collection.
//
// Returns an error if any batch write to MongoDB fails. Parse errors for
// individual lines are logged but do not prevent other lines from being processed.
//
// This is a convenience method for callers that have multiple lines available
// at once but still want synchronous confirmation of the writes.
func (ig *Ingester) IngestBatch(ctx context.Context, lines []string) error {
	var (
		userModels  []mongo.WriteModel
		eventModels []mongo.WriteModel
		deadModels  []mongo.WriteModel
	)

	for _, line := range lines {
		rec, err := ig.parser.ParseLine(line)
		if err != nil {
			ig.logger.WithError(err).Debug("ingest batch: invalid line")
			deadModels = append(deadModels, store.DeadLetterModel(line, err))
			continue
		}

		userID, err := ig.store.Identity().Resolve(ctx, rec.AccountID, rec.DistinctID)
		if err != nil {
			ig.logger.WithError(err).Warn("ingest batch: identity resolve failed")
			deadModels = append(deadModels, store.DeadLetterModel(line, err))
			continue
		}

		switch rec.Category() {
		case talog.CategoryUser:
			userModels = append(userModels, store.UserWriteModel(rec.Type, userID, rec.Doc))
		case talog.CategoryEvent:
			rec.Doc["#user_id"] = userID
			eventModels = append(eventModels, store.EventWriteModel(rec.Type, rec.UUID, rec.Doc))
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
