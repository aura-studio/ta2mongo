package batch

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestIngest_TrackEvent(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 12:00:00","#uuid":"ingest-evt-1","#account_id":"acc_ingest_1","properties":{"ip":"1.2.3.4","browser":"Chrome"}}`
	tt.single(line)

	// Verify event was written
	var doc bson.M
	err := tt.db.Collection("event").FindOne(ctx, bson.M{"#uuid": "ingest-evt-1"}).Decode(&doc)
	if err != nil {
		t.Fatalf("FindOne event: %v", err)
	}
	if doc["#event_name"] != "login" {
		t.Errorf("expected event_name=login, got %v", doc["#event_name"])
	}
	if doc["ip"] != "1.2.3.4" {
		t.Errorf("expected ip=1.2.3.4, got %v", doc["ip"])
	}

	// Verify user identity was created
	count, err := tt.db.Collection("id_mapping").CountDocuments(ctx, bson.M{"#account_id": "acc_ingest_1"})
	if err != nil {
		t.Fatalf("count id_mapping: %v", err)
	}
	if count == 0 {
		t.Error("expected id_mapping entry for acc_ingest_1")
	}

	// Verify #user_id was set on the event
	if doc["#user_id"] == nil {
		t.Error("expected #user_id to be set on event")
	}
}

func TestIngest_UserSet(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	line := `{"#type":"user_set","#time":"2024-01-01 12:00:00","#uuid":"ingest-user-1","#account_id":"acc_user_1","properties":{"name":"Alice","age":30}}`
	tt.single(line)

	// Verify user document
	var doc bson.M
	err := tt.db.Collection("user").FindOne(ctx, bson.M{"name": "Alice"}).Decode(&doc)
	if err != nil {
		t.Fatalf("FindOne user: %v", err)
	}
	if doc["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", doc["name"])
	}
}

func TestIngest_UserDel(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	// First create user
	setLine := `{"#type":"user_set","#time":"2024-01-01","#uuid":"del-u1","#account_id":"acc_del_1","properties":{"name":"ToDelete"}}`
	tt.single(setLine)

	// Verify user exists
	count, _ := tt.db.Collection("user").CountDocuments(ctx, bson.M{"name": "ToDelete"})
	if count != 1 {
		t.Fatalf("expected 1 user before delete, got %d", count)
	}

	// Delete user
	delLine := `{"#type":"user_del","#time":"2024-01-02","#uuid":"del-u2","#account_id":"acc_del_1"}`
	tt.single(delLine)

	// Verify user is deleted
	count, _ = tt.db.Collection("user").CountDocuments(ctx, bson.M{"name": "ToDelete"})
	if count != 0 {
		t.Errorf("expected 0 users after delete, got %d", count)
	}
}

func TestIngest_InvalidLine_GoesToDeadLetter(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	// Per-line parse failures are routed to dead_letter, not returned as errors.
	tt.single("this is not json")

	count, cerr := tt.db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if cerr != nil {
		t.Fatalf("count dead_letter: %v", cerr)
	}
	if count != 1 {
		t.Errorf("expected 1 dead letter, got %d", count)
	}
}

func TestIngest_NotTAPayload_GoesToDeadLetter(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	tt.single(`{"foo":"bar"}`)

	count, _ := tt.db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if count != 1 {
		t.Errorf("expected 1 dead letter, got %d", count)
	}
}
