package store

import (
	"context"
	"fmt"
	"sync"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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

	// In-memory caches. Entries are never evicted because bindings are permanent.
	// accountCache:  account_id (string) → user_id (int64)
	// distinctCache: distinct_id (string) → user_id (int64)
	// distinctBound: distinct_id (string) → true (means already bound to an account_id)
	accountCache  sync.Map
	distinctCache sync.Map
	distinctBound sync.Map
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

// MappingCollection returns the id_mapping collection handle.
func (ir *IdentityResolver) MappingCollection() *mongo.Collection {
	return ir.mapping
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

// ---------------------------------------------------------------------------
// Fast path: in-memory cache lookup
// ---------------------------------------------------------------------------

// tryCache attempts to resolve user_id purely from in-memory cache.
// Returns (user_id, true) on full cache hit, (0, false) if DB access needed.
func (ir *IdentityResolver) tryCache(accountID, distinctID string) (int64, bool) {
	if accountID != "" && distinctID != "" {
		aUID, aOk := ir.accountCache.Load(accountID)
		_, dOk := ir.distinctCache.Load(distinctID)
		if aOk && dOk {
			// Both known, return account_id's user_id (priority rule).
			return aUID.(int64), true
		}
		// One or both unknown → need DB for potential binding.
		return 0, false
	}

	if accountID != "" {
		if uid, ok := ir.accountCache.Load(accountID); ok {
			return uid.(int64), true
		}
		return 0, false
	}

	if distinctID != "" {
		if uid, ok := ir.distinctCache.Load(distinctID); ok {
			return uid.(int64), true
		}
		return 0, false
	}

	return 0, false
}

// ---------------------------------------------------------------------------
// Slow path: MongoDB operations with atomic guarantees
// ---------------------------------------------------------------------------

func (ir *IdentityResolver) resolveFromDB(ctx context.Context, accountID, distinctID string) (int64, error) {
	// Load current state from MongoDB.
	var accountMapping, distinctMapping *IDMapping
	var err error

	if accountID != "" {
		accountMapping, err = ir.findByAccountID(ctx, accountID)
		if err != nil {
			return 0, err
		}
	}

	if distinctID != "" {
		distinctMapping, err = ir.findByDistinctID(ctx, distinctID)
		if err != nil {
			return 0, err
		}
	}

	// Case 1: Only account_id
	if accountID != "" && distinctID == "" {
		if accountMapping != nil {
			ir.cacheMapping(accountMapping)
			return accountMapping.UserID, nil
		}
		return ir.atomicCreateForAccountID(ctx, accountID)
	}

	// Case 2: Only distinct_id
	if accountID == "" && distinctID != "" {
		if distinctMapping != nil {
			ir.cacheMapping(distinctMapping)
			return distinctMapping.UserID, nil
		}
		return ir.atomicCreateForDistinctID(ctx, distinctID)
	}

	// Case 3: Both present
	return ir.resolveWithBoth(ctx, accountID, distinctID, accountMapping, distinctMapping)
}

func (ir *IdentityResolver) resolveWithBoth(
	ctx context.Context,
	accountID, distinctID string,
	accountMapping, distinctMapping *IDMapping,
) (int64, error) {
	accountExists := accountMapping != nil
	distinctExists := distinctMapping != nil

	switch {
	case !accountExists && !distinctExists:
		// Both new → bind together, create new user_id.
		return ir.atomicCreateForBoth(ctx, accountID, distinctID)

	case accountExists && !distinctExists:
		// account_id exists, distinct_id is new → bind distinct_id to account's user.
		if err := ir.atomicBindDistinctToAccount(ctx, accountMapping.UserID, distinctID); err != nil {
			return 0, fmt.Errorf("bind distinct_id to account: %w", err)
		}
		ir.cacheMapping(accountMapping)
		ir.distinctCache.Store(distinctID, accountMapping.UserID)
		return accountMapping.UserID, nil

	case !accountExists && distinctExists:
		// account_id is new, distinct_id exists.
		if distinctMapping.AccountID == "" {
			// distinct_id not yet bound → try atomic bind.
			bound, err := ir.atomicBindAccountToDistinct(ctx, distinctMapping.UserID, accountID)
			if err != nil {
				return 0, err
			}
			if bound {
				// Successful bind.
				ir.accountCache.Store(accountID, distinctMapping.UserID)
				ir.distinctBound.Store(distinctID, true)
				return distinctMapping.UserID, nil
			}
			// Another pod bound it first → account_id needs its own user.
			return ir.atomicCreateForAccountID(ctx, accountID)
		}
		// distinct_id already bound to another account → no bind.
		return ir.atomicCreateForAccountID(ctx, accountID)

	default:
		// Both exist → no binding change. Priority: account_id's user_id.
		ir.cacheMapping(accountMapping)
		ir.cacheMapping(distinctMapping)
		return accountMapping.UserID, nil
	}
}

// ---------------------------------------------------------------------------
// Atomic MongoDB operations (multi-pod safe)
// ---------------------------------------------------------------------------

// nextUserID atomically increments and returns the next #user_id.
func (ir *IdentityResolver) nextUserID(ctx context.Context) (int64, error) {
	filter := bson.M{"_id": "user_id"}
	update := bson.M{"$inc": bson.M{"seq": int64(1)}}
	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var result struct {
		Seq int64 `bson:"seq"`
	}
	err := ir.counter.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)
	if err != nil {
		return 0, err
	}
	return result.Seq, nil
}

// atomicCreateForAccountID creates a new mapping for a new account_id.
// If another pod races and inserts first (DuplicateKeyError on #account_id),
// we fall back to reading the existing record.
func (ir *IdentityResolver) atomicCreateForAccountID(ctx context.Context, accountID string) (int64, error) {
	userID, err := ir.nextUserID(ctx)
	if err != nil {
		return 0, err
	}

	doc := IDMapping{
		UserID:      userID,
		AccountID:   accountID,
		DistinctIDs: nil,
	}
	_, err = ir.mapping.InsertOne(ctx, doc)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Another pod created it first → read theirs.
			existing, findErr := ir.findByAccountID(ctx, accountID)
			if findErr != nil {
				return 0, findErr
			}
			if existing != nil {
				ir.cacheMapping(existing)
				return existing.UserID, nil
			}
			// Should not happen if index works correctly.
			return 0, err
		}
		return 0, err
	}

	ir.accountCache.Store(accountID, userID)
	return userID, nil
}

// atomicCreateForDistinctID creates a new mapping for a new distinct_id.
// Uses $addToSet-style check: we insert a new doc. If the distinct_id was
// concurrently added by another pod to a different doc, we detect it via
// a post-insert query.
func (ir *IdentityResolver) atomicCreateForDistinctID(ctx context.Context, distinctID string) (int64, error) {
	userID, err := ir.nextUserID(ctx)
	if err != nil {
		return 0, err
	}

	doc := IDMapping{
		UserID:      userID,
		AccountID:   "",
		DistinctIDs: []string{distinctID},
	}
	_, err = ir.mapping.InsertOne(ctx, doc)
	if err != nil {
		return 0, err
	}

	// Verify no race: check if distinct_id now appears in multiple docs.
	// If another pod inserted concurrently with the same distinct_id, keep
	// the one with the smaller user_id and delete ours.
	existing, findErr := ir.findByDistinctID(ctx, distinctID)
	if findErr != nil {
		return 0, findErr
	}
	if existing != nil && existing.UserID != userID {
		// Race detected — another pod won. Remove our orphan doc.
		if _, delErr := ir.mapping.DeleteOne(ctx, bson.M{"#user_id": userID}); delErr != nil {
			return 0, fmt.Errorf("delete orphan mapping after race: %w", delErr)
		}
		ir.cacheMapping(existing)
		return existing.UserID, nil
	}

	ir.distinctCache.Store(distinctID, userID)
	return userID, nil
}

// atomicCreateForBoth creates a new mapping with both IDs bound together.
// Handles races on both account_id (unique index) and distinct_id.
func (ir *IdentityResolver) atomicCreateForBoth(ctx context.Context, accountID, distinctID string) (int64, error) {
	userID, err := ir.nextUserID(ctx)
	if err != nil {
		return 0, err
	}

	doc := IDMapping{
		UserID:      userID,
		AccountID:   accountID,
		DistinctIDs: []string{distinctID},
	}
	_, err = ir.mapping.InsertOne(ctx, doc)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// account_id unique index conflict → another pod created it.
			existing, findErr := ir.findByAccountID(ctx, accountID)
			if findErr != nil {
				return 0, findErr
			}
			if existing != nil {
				// Try to bind distinct_id to the existing mapping.
				if bindErr := ir.atomicBindDistinctToAccount(ctx, existing.UserID, distinctID); bindErr != nil {
					return 0, fmt.Errorf("bind distinct_id after race: %w", bindErr)
				}
				ir.cacheMapping(existing)
				ir.distinctCache.Store(distinctID, existing.UserID)
				return existing.UserID, nil
			}
			return 0, err
		}
		return 0, err
	}

	ir.accountCache.Store(accountID, userID)
	ir.distinctCache.Store(distinctID, userID)
	ir.distinctBound.Store(distinctID, true)
	return userID, nil
}

// atomicBindDistinctToAccount adds a distinct_id to an existing user's mapping.
// $addToSet is naturally idempotent, safe for concurrent execution.
func (ir *IdentityResolver) atomicBindDistinctToAccount(ctx context.Context, userID int64, distinctID string) error {
	_, err := ir.mapping.UpdateOne(ctx,
		bson.M{"#user_id": userID},
		bson.M{"$addToSet": bson.M{"#distinct_ids": distinctID}},
	)
	return err
}

// atomicBindAccountToDistinct attempts to set account_id on a mapping that
// currently has no account bound. Uses a conditional update to prevent races.
// Returns true if this pod successfully performed the bind, false if another
// pod bound a different account_id first.
func (ir *IdentityResolver) atomicBindAccountToDistinct(ctx context.Context, userID int64, accountID string) (bool, error) {
	// Condition: only bind if #account_id is currently empty.
	filter := bson.M{
		"#user_id": userID,
		"$or": []bson.M{
			{"#account_id": ""},
			{"#account_id": bson.M{"$exists": false}},
		},
	}
	update := bson.M{"$set": bson.M{"#account_id": accountID}}

	result, err := ir.mapping.UpdateOne(ctx, filter, update)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Another pod already has this account_id on a different doc.
			return false, nil
		}
		return false, err
	}

	return result.ModifiedCount > 0, nil
}

// ---------------------------------------------------------------------------
// Query helpers
// ---------------------------------------------------------------------------

func (ir *IdentityResolver) findByAccountID(ctx context.Context, accountID string) (*IDMapping, error) {
	var m IDMapping
	err := ir.mapping.FindOne(ctx, bson.M{"#account_id": accountID}).Decode(&m)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (ir *IdentityResolver) findByDistinctID(ctx context.Context, distinctID string) (*IDMapping, error) {
	var m IDMapping
	err := ir.mapping.FindOne(ctx, bson.M{"#distinct_ids": distinctID}).Decode(&m)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ---------------------------------------------------------------------------
// Cache helpers
// ---------------------------------------------------------------------------

// cacheMapping populates the in-memory caches from a loaded IDMapping.
func (ir *IdentityResolver) cacheMapping(m *IDMapping) {
	if m == nil {
		return
	}
	if m.AccountID != "" {
		ir.accountCache.Store(m.AccountID, m.UserID)
	}
	for _, did := range m.DistinctIDs {
		ir.distinctCache.Store(did, m.UserID)
		if m.AccountID != "" {
			ir.distinctBound.Store(did, true)
		}
	}
}
