package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ---------------------------------------------------------------------------
// EnsureIndexes
// ---------------------------------------------------------------------------

func TestEnsureIndexes_Integration(t *testing.T) {
	st, db, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// Verify user collection indexes
	userIndexes, err := listIndexNames(ctx, db.Collection("user"))
	if err != nil {
		t.Fatalf("list user indexes: %v", err)
	}
	assertContains(t, userIndexes, "#user_id_1", "user collection should have #user_id index")

	// Verify event collection indexes
	eventIndexes, err := listIndexNames(ctx, db.Collection("event"))
	if err != nil {
		t.Fatalf("list event indexes: %v", err)
	}
	assertContains(t, eventIndexes, "#uuid_1", "event collection should have #uuid index")

	// Verify id_mapping collection indexes
	mappingIndexes, err := listIndexNames(ctx, db.Collection("id_mapping"))
	if err != nil {
		t.Fatalf("list id_mapping indexes: %v", err)
	}
	assertContains(t, mappingIndexes, "#user_id_1", "id_mapping should have #user_id index")
	assertContains(t, mappingIndexes, "#account_id_1", "id_mapping should have #account_id index")

	// Idempotent: call again should not fail
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}
}

// ---------------------------------------------------------------------------
// BulkWrite with real MongoDB
// ---------------------------------------------------------------------------

func TestBulkWrite_InsertDocuments(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	models := []mongo.WriteModel{
		mongo.NewInsertOneModel().SetDocument(bson.M{
			"#uuid":   "test-uuid-1",
			"#type":   "track",
			"_ts":     time.Now().UnixNano(),
			"field_a": "value_a",
		}),
		mongo.NewInsertOneModel().SetDocument(bson.M{
			"#uuid":   "test-uuid-2",
			"#type":   "track",
			"_ts":     time.Now().UnixNano(),
			"field_b": "value_b",
		}),
	}

	if err := st.BulkWrite(ctx, st.EventCollection(), models); err != nil {
		t.Fatalf("BulkWrite: %v", err)
	}

	// Verify documents were inserted
	count, err := st.EventCollection().CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 documents, got %d", count)
	}
}

func TestBulkWrite_EmptyModels(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	// Should be a no-op
	if err := st.BulkWrite(ctx, st.EventCollection(), nil); err != nil {
		t.Fatalf("BulkWrite with nil models: %v", err)
	}
	if err := st.BulkWrite(ctx, st.EventCollection(), []mongo.WriteModel{}); err != nil {
		t.Fatalf("BulkWrite with empty models: %v", err)
	}
}

func TestBulkWriteOrdered_InsertDocuments(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := time.Now().UnixNano()
	models := []mongo.WriteModel{
		UserWriteModel("user_set", 1, bson.M{
			"#time": "2024-01-01",
			"_ts":   ts,
			"name":  "Alice",
		}),
	}

	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), models); err != nil {
		t.Fatalf("BulkWriteOrdered: %v", err)
	}

	count, err := st.UserCollection().CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 document, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// WriteStats tracking
// ---------------------------------------------------------------------------

func TestWriteStats_Initial(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	if st.Stats().TotalRetries() != 0 {
		t.Errorf("expected 0 retries initially, got %d", st.Stats().TotalRetries())
	}
}

// ---------------------------------------------------------------------------
// DeadLetter collection
// ---------------------------------------------------------------------------

func TestDeadLetter_Write(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	models := []mongo.WriteModel{
		DeadLetterModel(`{"invalid":"json"}`, nil),
		DeadLetterModel(`bad line`, fmt.Errorf("parse error")),
	}

	if err := st.BulkWrite(ctx, st.DeadLetterCollection(), models); err != nil {
		t.Fatalf("BulkWrite dead letter: %v", err)
	}

	count, err := st.DeadLetterCollection().CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 dead letters, got %d", count)
	}

	// Verify error field is stored
	var doc bson.M
	err = st.DeadLetterCollection().FindOne(ctx, bson.M{"error": "parse error"}).Decode(&doc)
	if err != nil {
		t.Fatalf("find dead letter with error: %v", err)
	}
	if doc["line"] != "bad line" {
		t.Errorf("expected line='bad line', got %v", doc["line"])
	}
}

// ---------------------------------------------------------------------------
// Collection accessor tests
// ---------------------------------------------------------------------------

func TestCollectionAccessors(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	if st.UserCollection() == nil {
		t.Error("UserCollection should not be nil")
	}
	if st.EventCollection() == nil {
		t.Error("EventCollection should not be nil")
	}
	if st.DeadLetterCollection() == nil {
		t.Error("DeadLetterCollection should not be nil")
	}
	if st.Identity() == nil {
		t.Error("Identity should not be nil")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func listIndexNames(ctx context.Context, coll *mongo.Collection) ([]string, error) {
	cursor, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	var indexes []struct {
		Name string `bson:"name"`
	}
	if err := cursor.All(ctx, &indexes); err != nil {
		return nil, err
	}
	names := make([]string, len(indexes))
	for i, idx := range indexes {
		names[i] = idx.Name
	}
	return names, nil
}

func assertContains(t *testing.T, slice []string, item string, msg string) {
	t.Helper()
	for _, s := range slice {
		if s == item {
			return
		}
	}
	t.Errorf("%s: %q not found in %v", msg, item, slice)
}

// suppress unused import
var _ = fmt.Sprintf
