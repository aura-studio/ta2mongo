// Package store provides MongoDB persistence for ThinkingData records,
// including index management, write-model building, and bulk writes with retry.
package store

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/aura-studio/tango/internal/logging"

	"github.com/cenkalti/backoff/v4"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Config is the store's own configuration — the subset of settings the store
// actually uses, owned here instead of depending on the top-level config
// package. Callers (e.g. dao) project the loaded configuration onto it.
type Config struct {
	// MaxElapsedTime is the maximum total retry time for a single bulk write.
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding works.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".maxElapsedTime", "0s")
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
		if err != nil && isOnlyDuplicateKey(err) {
			// Benign: the _ts filter-guard write models (see writemodel.go) skip a
			// record whose target already holds a newer _ts by failing to match,
			// after which the upsert's insert attempt conflicts with the unique key
			// (E11000). These are intended no-ops, not failures — don't retry or
			// report them.
			return nil
		}
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

// isOnlyDuplicateKey reports whether a bulk-write error consists solely of
// duplicate-key (E11000) write errors with no write-concern error. The _ts
// filter-guard write models (see writemodel.go) rely on this: when a newer
// document already exists, the guarded upsert fails to match and its insert
// attempt conflicts with the unique key, surfacing as E11000. Such errors mean
// "skipped, a newer record won" — they are intended no-ops, not failures.
func isOnlyDuplicateKey(err error) bool {
	var bwe mongo.BulkWriteException
	if !errors.As(err, &bwe) {
		return false
	}
	if bwe.WriteConcernError != nil || len(bwe.WriteErrors) == 0 {
		return false
	}
	for _, we := range bwe.WriteErrors {
		if we.Code != 11000 {
			return false
		}
	}
	return true
}
