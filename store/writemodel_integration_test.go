package store

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// ---------------------------------------------------------------------------
// UserWriteModel integration tests — verify actual MongoDB behavior
// ---------------------------------------------------------------------------

func TestUserWriteModel_UserSet_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts1 := time.Now().UnixNano()
	model := UserWriteModel("user_set", 100, bson.M{
		"#time": "2024-01-01",
		"_ts":   ts1,
		"name":  "Alice",
		"age":   float64(30),
	})

	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("BulkWriteOrdered: %v", err)
	}

	var doc bson.M
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": int64(100)}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", doc["name"])
	}

	// Update with newer timestamp: should overwrite
	ts2 := ts1 + 1000
	model2 := UserWriteModel("user_set", 100, bson.M{
		"#time": "2024-01-02",
		"_ts":   ts2,
		"name":  "Bob",
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model2}); err != nil {
		t.Fatalf("BulkWriteOrdered update: %v", err)
	}

	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": int64(100)}).Decode(&doc); err != nil {
		t.Fatalf("FindOne after update: %v", err)
	}
	if doc["name"] != "Bob" {
		t.Errorf("expected name=Bob after update, got %v", doc["name"])
	}

	// Update with older timestamp: should NOT overwrite
	ts3 := ts1 - 1000
	model3 := UserWriteModel("user_set", 100, bson.M{
		"#time": "2023-12-31",
		"_ts":   ts3,
		"name":  "OldName",
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model3}); err != nil {
		t.Fatalf("BulkWriteOrdered old: %v", err)
	}

	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": int64(100)}).Decode(&doc); err != nil {
		t.Fatalf("FindOne after old update: %v", err)
	}
	if doc["name"] != "Bob" {
		t.Errorf("expected name still Bob (older ts should not overwrite), got %v", doc["name"])
	}
}

func TestUserWriteModel_UserSetOnce_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()
	model := UserWriteModel("user_setOnce", 200, bson.M{
		"#time":    "2024-01-01",
		"_ts":      ts,
		"nickname": "First",
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("BulkWriteOrdered: %v", err)
	}

	// Second setOnce: should not overwrite existing value
	model2 := UserWriteModel("user_setOnce", 200, bson.M{
		"#time":    "2024-01-02",
		"_ts":      ts + 1000,
		"nickname": "Second",
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model2}); err != nil {
		t.Fatalf("BulkWriteOrdered second: %v", err)
	}

	var doc bson.M
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": int64(200)}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["nickname"] != "First" {
		t.Errorf("expected nickname=First (setOnce should not overwrite), got %v", doc["nickname"])
	}
}

func TestUserWriteModel_UserAdd_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()

	// First add: creates user with coins=100
	model := UserWriteModel("user_add", 300, bson.M{
		"#time": "2024-01-01",
		"_ts":   ts,
		"coins": float64(100),
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("BulkWriteOrdered: %v", err)
	}

	// Second add: increments coins by 50
	model2 := UserWriteModel("user_add", 300, bson.M{
		"#time": "2024-01-02",
		"_ts":   ts + 1000,
		"coins": float64(50),
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model2}); err != nil {
		t.Fatalf("BulkWriteOrdered second: %v", err)
	}

	var doc bson.M
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": int64(300)}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}

	// coins should be 150 (100 + 50)
	coins, ok := doc["coins"].(float64)
	if !ok {
		// Try int types
		switch v := doc["coins"].(type) {
		case int32:
			coins = float64(v)
			ok = true
		case int64:
			coins = float64(v)
			ok = true
		default:
			t.Fatalf("unexpected coins type: %T = %v", doc["coins"], doc["coins"])
		}
	}
	if coins != 150 {
		t.Errorf("expected coins=150, got %v", coins)
	}
}

func TestUserWriteModel_UserDel_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()

	// Create user
	model := UserWriteModel("user_set", 400, bson.M{
		"#time": "2024-01-01",
		"_ts":   ts,
		"name":  "ToDelete",
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// Verify user exists
	count, err := st.UserCollection().CountDocuments(ctx, bson.M{"#user_id": int64(400)})
	if err != nil {
		t.Fatalf("count before delete: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 doc before delete, got %d", count)
	}

	// Delete user
	delModel := UserWriteModel("user_del", 400, bson.M{
		"#time": "2024-01-02",
		"_ts":   ts + 1000,
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{delModel}); err != nil {
		t.Fatalf("delete user: %v", err)
	}

	// Verify user is deleted
	count, err = st.UserCollection().CountDocuments(ctx, bson.M{"#user_id": int64(400)})
	if err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 docs after delete, got %d", count)
	}
}

func TestUserWriteModel_UserAppend_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()

	// Append tags
	model := UserWriteModel("user_append", 500, bson.M{
		"#time": "2024-01-01",
		"_ts":   ts,
		"tags":  []any{"vip"},
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("BulkWriteOrdered append: %v", err)
	}

	// Append more tags
	model2 := UserWriteModel("user_append", 500, bson.M{
		"#time": "2024-01-02",
		"_ts":   ts + 1000,
		"tags":  []any{"premium"},
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model2}); err != nil {
		t.Fatalf("BulkWriteOrdered append second: %v", err)
	}

	var doc bson.M
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": int64(500)}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}

	tags, ok := doc["tags"].(bson.A)
	if !ok {
		t.Fatalf("expected tags to be array, got %T = %v", doc["tags"], doc["tags"])
	}
	if len(tags) != 2 {
		t.Errorf("expected 2 tags, got %d: %v", len(tags), tags)
	}
}

func TestUserWriteModel_UserUniqAppend_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()

	// Uniq append
	model := UserWriteModel("user_uniq_append", 600, bson.M{
		"#time": "2024-01-01",
		"_ts":   ts,
		"tags":  []any{"vip"},
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("BulkWriteOrdered: %v", err)
	}

	// Uniq append same value: should not duplicate
	model2 := UserWriteModel("user_uniq_append", 600, bson.M{
		"#time": "2024-01-02",
		"_ts":   ts + 1000,
		"tags":  []any{"vip"},
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model2}); err != nil {
		t.Fatalf("BulkWriteOrdered second: %v", err)
	}

	// Uniq append different value
	model3 := UserWriteModel("user_uniq_append", 600, bson.M{
		"#time": "2024-01-03",
		"_ts":   ts + 2000,
		"tags":  []any{"premium"},
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model3}); err != nil {
		t.Fatalf("BulkWriteOrdered third: %v", err)
	}

	var doc bson.M
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": int64(600)}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}

	tags, ok := doc["tags"].(bson.A)
	if !ok {
		t.Fatalf("expected tags array, got %T = %v", doc["tags"], doc["tags"])
	}
	// Should have exactly 2 unique tags
	if len(tags) != 2 {
		t.Errorf("expected 2 unique tags, got %d: %v", len(tags), tags)
	}
}

func TestUserWriteModel_UserUnset_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()

	// Create user with fields
	model := UserWriteModel("user_set", 700, bson.M{
		"#time":     "2024-01-01",
		"_ts":       ts,
		"name":      "Alice",
		"old_field": "to_remove",
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Unset old_field with newer ts
	unsetModel := UserWriteModel("user_unset", 700, bson.M{
		"#time":     "2024-01-02",
		"_ts":       ts + 1000,
		"old_field": true,
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{unsetModel}); err != nil {
		t.Fatalf("unset: %v", err)
	}

	var doc bson.M
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": int64(700)}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if _, ok := doc["old_field"]; ok {
		t.Errorf("expected old_field to be unset, but it still exists: %v", doc["old_field"])
	}
	if doc["name"] != "Alice" {
		t.Errorf("expected name=Alice to remain, got %v", doc["name"])
	}
}

// ---------------------------------------------------------------------------
// EventWriteModel integration tests
// ---------------------------------------------------------------------------

func TestEventWriteModel_Track_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	model := EventWriteModel("track", "evt-uuid-1", bson.M{
		"#uuid":       "evt-uuid-1",
		"#event_name": "login",
		"#time":       "2024-01-01",
		"_ts":         time.Now().UnixNano(),
		"#user_id":    int64(1),
		"ip":          "1.2.3.4",
	})

	if err := st.BulkWrite(ctx, st.EventCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("BulkWrite: %v", err)
	}

	var doc bson.M
	if err := st.EventCollection().FindOne(ctx, bson.M{"#uuid": "evt-uuid-1"}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["#event_name"] != "login" {
		t.Errorf("expected event_name=login, got %v", doc["#event_name"])
	}
	if doc["ip"] != "1.2.3.4" {
		t.Errorf("expected ip=1.2.3.4, got %v", doc["ip"])
	}
}

func TestEventWriteModel_TrackUpdate_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts1 := time.Now().UnixNano()

	// Insert initial event
	model := EventWriteModel("track", "evt-uuid-upd", bson.M{
		"#uuid":       "evt-uuid-upd",
		"#event_name": "purchase",
		"#time":       "2024-01-01",
		"_ts":         ts1,
		"amount":      float64(100),
		"status":      "pending",
	})
	if err := st.BulkWrite(ctx, st.EventCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// track_update with newer ts: update status field
	ts2 := ts1 + 1000
	updateModel := EventWriteModel("track_update", "evt-uuid-upd", bson.M{
		"#uuid":       "evt-uuid-upd",
		"#event_name": "purchase",
		"#time":       "2024-01-01",
		"_ts":         ts2,
		"status":      "completed",
	})
	if err := st.BulkWrite(ctx, st.EventCollection(), []mongo.WriteModel{updateModel}); err != nil {
		t.Fatalf("track_update: %v", err)
	}

	var doc bson.M
	if err := st.EventCollection().FindOne(ctx, bson.M{"#uuid": "evt-uuid-upd"}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["status"] != "completed" {
		t.Errorf("expected status=completed after track_update, got %v", doc["status"])
	}
}

func TestEventWriteModel_TrackOverwrite_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts1 := time.Now().UnixNano()

	// Insert initial event with multiple fields
	model := EventWriteModel("track", "evt-uuid-ow", bson.M{
		"#uuid":       "evt-uuid-ow",
		"#event_name": "purchase",
		"#time":       "2024-01-01",
		"_ts":         ts1,
		"amount":      float64(100),
		"status":      "pending",
		"extra":       "field",
	})
	if err := st.BulkWrite(ctx, st.EventCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	// track_overwrite replaces the entire document
	ts2 := ts1 + 1000
	overwriteModel := EventWriteModel("track_overwrite", "evt-uuid-ow", bson.M{
		"#uuid":       "evt-uuid-ow",
		"#event_name": "purchase",
		"#time":       "2024-01-02",
		"_ts":         ts2,
		"amount":      float64(200),
		"status":      "refunded",
	})
	if err := st.BulkWrite(ctx, st.EventCollection(), []mongo.WriteModel{overwriteModel}); err != nil {
		t.Fatalf("track_overwrite: %v", err)
	}

	var doc bson.M
	if err := st.EventCollection().FindOne(ctx, bson.M{"#uuid": "evt-uuid-ow"}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["status"] != "refunded" {
		t.Errorf("expected status=refunded after overwrite, got %v", doc["status"])
	}
	// After overwrite, "extra" field should be gone (full replacement)
	if _, ok := doc["extra"]; ok {
		t.Errorf("expected 'extra' field to be removed after overwrite, but it exists: %v", doc["extra"])
	}
}

// ---------------------------------------------------------------------------
// DeadLetterModel integration
// ---------------------------------------------------------------------------

func TestDeadLetterModel_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	model := DeadLetterModel("broken json line", nil)
	if err := st.BulkWrite(ctx, st.DeadLetterCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("write dead letter: %v", err)
	}

	var doc bson.M
	if err := st.DeadLetterCollection().FindOne(ctx, bson.M{"line": "broken json line"}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["error"] != "" {
		t.Errorf("expected empty error for nil parseErr, got %v", doc["error"])
	}
}

// ---------------------------------------------------------------------------
// Batch operations
// ---------------------------------------------------------------------------

func TestBulkWrite_MixedUserOperations(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()
	models := []mongo.WriteModel{
		UserWriteModel("user_set", 801, bson.M{"#time": "2024-01-01", "_ts": ts, "name": "User1"}),
		UserWriteModel("user_set", 802, bson.M{"#time": "2024-01-01", "_ts": ts, "name": "User2"}),
		UserWriteModel("user_set", 803, bson.M{"#time": "2024-01-01", "_ts": ts, "name": "User3"}),
	}

	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), models); err != nil {
		t.Fatalf("BulkWriteOrdered batch: %v", err)
	}

	count, err := st.UserCollection().CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 users, got %d", count)
	}
}

func TestBulkWrite_MultipleEvents(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()
	models := []mongo.WriteModel{
		EventWriteModel("track", "batch-evt-1", bson.M{"#uuid": "batch-evt-1", "#event_name": "e1", "_ts": ts}),
		EventWriteModel("track", "batch-evt-2", bson.M{"#uuid": "batch-evt-2", "#event_name": "e2", "_ts": ts}),
		EventWriteModel("track", "batch-evt-3", bson.M{"#uuid": "batch-evt-3", "#event_name": "e3", "_ts": ts}),
	}

	if err := st.BulkWrite(ctx, st.EventCollection(), models); err != nil {
		t.Fatalf("BulkWrite events: %v", err)
	}

	count, err := st.EventCollection().CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3 events, got %d", count)
	}
}
