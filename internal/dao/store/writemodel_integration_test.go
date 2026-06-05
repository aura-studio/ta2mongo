package store

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

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
