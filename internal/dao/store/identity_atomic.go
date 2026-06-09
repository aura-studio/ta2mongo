package store

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// nextUserID atomically increments and returns the next #user_id.
//
// FindOneAndUpdate(upsert) is atomic once the counter document exists, but its
// FIRST creation races: when several workers call this concurrently before the
// {_id:"user_id"} doc exists, each attempts the upsert-insert and all but one
// get E11000 (duplicate key on id_counter._id_). That is expected, not fatal —
// the loser simply retries, and the retry finds the now-existing doc and $inc's
// it. Without this retry the resolve failed and the line was dead-lettered: seen
// in production on DocumentDB under concurrent first-seen-user load (its upsert
// concurrency makes the collision far more likely than on stock MongoDB), where
// it silently sent events to dead_letter instead of writing them.
func (ir *IdentityResolver) nextUserID(ctx context.Context) (int64, error) {
	filter := bson.M{"_id": "user_id"}
	update := bson.M{"$inc": bson.M{"seq": int64(1)}}
	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var result struct {
		Seq int64 `bson:"seq"`
	}
	const maxAttempts = 8
	var err error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		err = ir.counter.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)
		if err == nil {
			return result.Seq, nil
		}
		// Only the cold-start upsert-insert race is retryable; anything else
		// (network, auth, ctx cancel) is returned immediately.
		if !mongo.IsDuplicateKeyError(err) {
			return 0, err
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
	}
	return 0, fmt.Errorf("nextUserID: counter upsert kept losing the create race after %d attempts: %w", maxAttempts, err)
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
		DistinctIDs: []string{},
	}
	_, err = ir.mapping.InsertOne(ctx, doc)
	if err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// Another pod created it first -> read theirs.
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

	// Verify no race: re-read by distinct_id. #distinct_ids has no unique index,
	// so a concurrent pod may have inserted a different doc with the same
	// distinct_id. If the lookup returns a doc other than the one we just created,
	// that doc wins: we drop our orphan and adopt the existing mapping. (The
	// winner is whichever doc the lookup returns, not a smaller-user_id rule.)
	existing, findErr := ir.findByDistinctID(ctx, distinctID)
	if findErr != nil {
		return 0, findErr
	}
	if existing != nil && existing.UserID != userID {
		// Race detected - another pod won. Remove our orphan doc.
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
			// account_id unique index conflict -> another pod created it.
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
