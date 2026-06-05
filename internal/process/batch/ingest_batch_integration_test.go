package batch

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestIngestBatch_MixedLines(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	lines := []string{
		`{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"batch-e1","#account_id":"batch_acc_1"}`,
		`{"#type":"track","#event_name":"click","#time":"2024-01-01","#uuid":"batch-e2","#account_id":"batch_acc_1"}`,
		`{"#type":"user_set","#time":"2024-01-01","#uuid":"batch-u1","#account_id":"batch_acc_1","properties":{"name":"Alice"}}`,
		"invalid line",
		`{"#type":"track","#event_name":"purchase","#time":"2024-01-01","#uuid":"batch-e3","#distinct_id":"batch_did_2","properties":{"amount":99.9}}`,
	}

	tt.batch(lines)

	// Verify events: 3 events
	eventCount, err := tt.db.Collection("event").CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 3 {
		t.Errorf("expected 3 events, got %d", eventCount)
	}

	// Verify users: 1 user_set
	userCount, err := tt.db.Collection("user").CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Errorf("expected 1 user, got %d", userCount)
	}

	// Verify dead letters: 1 invalid line
	dlCount, err := tt.db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count dead_letter: %v", err)
	}
	if dlCount != 1 {
		t.Errorf("expected 1 dead letter, got %d", dlCount)
	}
}

func TestIngestBatch_AllValid(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	lines := []string{
		`{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"all-valid-1","#account_id":"av1"}`,
		`{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"all-valid-2","#account_id":"av2"}`,
		`{"#type":"user_set","#time":"2024-01-01","#uuid":"all-valid-3","#account_id":"av3","properties":{"x":1}}`,
	}

	tt.batch(lines)

	eventCount, _ := tt.db.Collection("event").CountDocuments(ctx, bson.M{})
	userCount, _ := tt.db.Collection("user").CountDocuments(ctx, bson.M{})
	dlCount, _ := tt.db.Collection("dead_letter").CountDocuments(ctx, bson.M{})

	if eventCount != 2 {
		t.Errorf("expected 2 events, got %d", eventCount)
	}
	if userCount != 1 {
		t.Errorf("expected 1 user, got %d", userCount)
	}
	if dlCount != 0 {
		t.Errorf("expected 0 dead letters, got %d", dlCount)
	}
}

func TestIngestBatch_AllInvalid(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	lines := []string{
		"bad1",
		"bad2",
		`{"foo":"bar"}`,
	}

	tt.batch(lines)

	dlCount, _ := tt.db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if dlCount != 3 {
		t.Errorf("expected 3 dead letters, got %d", dlCount)
	}
}

func TestIngestBatch_Empty(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	// An empty / nil source must be a no-op, not an error.
	tt.batch(nil)
	tt.batch([]string{})
}
