// Package store provides MongoDB persistence for ThinkingData records,
// including index management, write-model building, and bulk writes with retry.
package store

import (
	"context"
	"sync/atomic"
	"time"

	"rocket-nano/tools/tango/internal/logging"

	"github.com/cenkalti/backoff/v4"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Config is the store's own configuration — the subset of settings the store
// actually uses, owned here instead of depending on the top-level config
// package. Callers (e.g. dao) project the loaded configuration onto it.
type Config struct {
	// MaxElapsedTime is the maximum total retry time for a single bulk write.
	MaxElapsedTime time.Duration
}

// ApplyDefaults fills unset store options.
func (c *Config) ApplyDefaults() {
	if c.MaxElapsedTime <= 0 {
		c.MaxElapsedTime = 10 * time.Second
	}
}

// WriteStats holds cumulative retry statistics for bulk writes.
type WriteStats struct {
	Retries atomic.Int64 // total retry attempts across all bulk writes
}

// TotalRetries returns the cumulative retry count.
func (s *WriteStats) TotalRetries() int64 { return s.Retries.Load() }

// Store manages MongoDB collections for user, event, and dead-letter data.
type Store struct {
	user       *mongo.Collection
	event      *mongo.Collection
	deadLetter *mongo.Collection
	identity   *IdentityResolver
	cfg        *Config
	stats      WriteStats
}

// New creates a Store backed by the given database.
func New(db *mongo.Database, cfg *Config) *Store {
	return &Store{
		user:       db.Collection("user"),
		event:      db.Collection("event"),
		deadLetter: db.Collection("dead_letter"),
		identity:   NewIdentityResolver(db),
		cfg:        cfg,
	}
}

// Identity returns the identity resolver for user ID resolution.
func (s *Store) Identity() *IdentityResolver { return s.identity }

// Stats returns a pointer to the cumulative write statistics.
func (s *Store) Stats() *WriteStats { return &s.stats }

// UserCollection returns the user collection handle.
func (s *Store) UserCollection() *mongo.Collection { return s.user }

// EventCollection returns the event collection handle.
func (s *Store) EventCollection() *mongo.Collection { return s.event }

// DeadLetterCollection returns the dead_letter collection handle.
func (s *Store) DeadLetterCollection() *mongo.Collection { return s.deadLetter }

// BulkWrite executes a batch of write models with exponential-backoff retry.
// It uses unordered writes so individual failures don't block others.
func (s *Store) BulkWrite(ctx context.Context, coll *mongo.Collection, models []mongo.WriteModel) error {
	return s.bulkWrite(ctx, coll, models, false)
}

// BulkWriteOrdered executes a batch of write models with exponential-backoff retry.
// It uses ordered writes to guarantee that operations are applied in the order they
// appear in the batch. This is critical for user collections where the same user_id
// may appear multiple times and the final document state must reflect the last operation.
func (s *Store) BulkWriteOrdered(ctx context.Context, coll *mongo.Collection, models []mongo.WriteModel) error {
	return s.bulkWrite(ctx, coll, models, true)
}

func (s *Store) bulkWrite(ctx context.Context, coll *mongo.Collection, models []mongo.WriteModel, ordered bool) error {
	if len(models) == 0 {
		return nil
	}

	var attempt int
	op := func() error {
		attempt++
		opts := options.BulkWrite().SetOrdered(ordered)
		_, err := coll.BulkWrite(ctx, models, opts)
		return err
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 200 * time.Millisecond
	bo.MaxInterval = 2 * time.Second
	bo.MaxElapsedTime = s.cfg.MaxElapsedTime
	bo.Reset()

	err := backoff.Retry(op, backoff.WithContext(bo, ctx))

	// Track retries (attempts beyond the first one).
	if retries := attempt - 1; retries > 0 {
		s.stats.Retries.Add(int64(retries))
	}

	if err != nil {
		logging.WithError(err).WithField("collection", coll.Name()).
			Warn("bulk write failed after retries")
		return err
	}
	return nil
}
