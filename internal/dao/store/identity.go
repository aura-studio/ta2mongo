package store

import (
	"context"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ---------------------------------------------------------------------------
// ID Mapping Collection Schema
// ---------------------------------------------------------------------------
//
// The id_mapping collection implements ThinkingData's user identification rules:
//   - Each document represents a unique TA user (#user_id).
//   - #account_id and #user_id are 1:1 (one account maps to exactly one user).
//   - One #account_id can bind multiple #distinct_id values.
//   - One #distinct_id can only bind to one #account_id.
//
// Multi-pod safety is guaranteed by MongoDB atomic operations (unique indexes +
// conditional updates), NOT by in-process mutexes. The in-memory cache (sync.Map)
// acts as a read-through cache that never needs invalidation because bindings are
// irreversible.
//
// Document structure:
//
//	{
//	  "#user_id":      int64,          // auto-increment unique user ID
//	  "#account_id":   string|null,    // bound account ID (unique sparse)
//	  "#distinct_ids": []string,       // list of bound distinct IDs
//	}
//
// Additional collection: id_counter (single document for auto-increment)
//
//	{ "_id": "user_id", "seq": int64 }

// IDMapping represents a record in the id_mapping collection.
type IDMapping struct {
	UserID      int64    `bson:"#user_id"`
	AccountID   string   `bson:"#account_id,omitempty"`
	DistinctIDs []string `bson:"#distinct_ids"`
}

// IdentityResolver handles ThinkingData user identification and ID binding.
// It uses an in-memory cache (sync.Map) for hot-path lookups and falls back
// to MongoDB with atomic operations on cache miss. Safe for multi-pod deployment.
type IdentityResolver struct {
	mapping *mongo.Collection
	counter *mongo.Collection

	// In-memory read-through caches keyed by id → user_id. Entries are never
	// evicted: bindings are permanent, so a cached value can never go stale.
	// Trade-off: memory grows unbounded with the number of distinct users seen
	// over the process lifetime (accountCache + distinctCache entries). This is
	// acceptable for bounded user bases; if a cap is ever needed, eviction is
	// safe because MongoDB stays the source of truth and a miss simply re-queries.
	//   accountCache:  account_id (string) → user_id (int64)
	//   distinctCache: distinct_id (string) → user_id (int64)
	accountCache  sync.Map
	distinctCache sync.Map
}

// NewIdentityResolver creates a resolver backed by the given database.
func NewIdentityResolver(db *mongo.Database) *IdentityResolver {
	return &IdentityResolver{
		mapping: db.Collection("id_mapping"),
		counter: db.Collection("id_counter"),
	}
}

// EnsureIndexes creates required indexes on the id_mapping collection.
func (ir *IdentityResolver) EnsureIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "#user_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{
			Keys:    bson.D{{Key: "#account_id", Value: 1}},
			Options: options.Index().SetUnique(true).SetSparse(true),
		},
		{
			Keys: bson.D{{Key: "#distinct_ids", Value: 1}},
		},
	}
	_, err := ir.mapping.Indexes().CreateMany(ctx, indexes)
	return err
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// Resolve determines the #user_id for a given record based on its #account_id
// and #distinct_id, following ThinkingData's user identification rules.
//
// Fast path: if both IDs are already cached, returns immediately with zero IO.
// Slow path: queries MongoDB and uses atomic operations for correctness across pods.
func (ir *IdentityResolver) Resolve(ctx context.Context, accountID, distinctID string) (int64, error) {
	// Fast path: pure cache hit.
	if userID, ok := ir.tryCache(accountID, distinctID); ok {
		return userID, nil
	}

	// Slow path: cache miss, go to MongoDB.
	return ir.resolveFromDB(ctx, accountID, distinctID)
}
