package store

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

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
