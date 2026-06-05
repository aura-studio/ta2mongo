package store

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

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
