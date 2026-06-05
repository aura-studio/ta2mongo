package store

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

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
