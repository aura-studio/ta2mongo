package batch

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestIngest_IdentityResolution_SameAccount(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	// Ingest two events for the same account
	line1 := `{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"id-res-1","#account_id":"id_res_acc"}`
	line2 := `{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"id-res-2","#account_id":"id_res_acc"}`
	tt.single(line1, line2)

	// Both events should have the same #user_id
	var doc1, doc2 bson.M
	if err := tt.db.Collection("event").FindOne(ctx, bson.M{"#uuid": "id-res-1"}).Decode(&doc1); err != nil {
		t.Fatalf("FindOne 1: %v", err)
	}
	if err := tt.db.Collection("event").FindOne(ctx, bson.M{"#uuid": "id-res-2"}).Decode(&doc2); err != nil {
		t.Fatalf("FindOne 2: %v", err)
	}

	if doc1["#user_id"] != doc2["#user_id"] {
		t.Errorf("expected same #user_id for same account, got %v and %v", doc1["#user_id"], doc2["#user_id"])
	}
}

func TestIngest_IdentityResolution_AccountAndDistinct(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	// First event with distinct_id only
	line1 := `{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"ad-1","#distinct_id":"ad_did"}`
	// Second event with both: should bind account to distinct's user
	line2 := `{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"ad-2","#account_id":"ad_acc","#distinct_id":"ad_did"}`
	tt.single(line1, line2)

	var doc1, doc2 bson.M
	tt.db.Collection("event").FindOne(ctx, bson.M{"#uuid": "ad-1"}).Decode(&doc1)
	tt.db.Collection("event").FindOne(ctx, bson.M{"#uuid": "ad-2"}).Decode(&doc2)

	if doc1["#user_id"] != doc2["#user_id"] {
		t.Errorf("expected same #user_id after binding, got %v and %v", doc1["#user_id"], doc2["#user_id"])
	}
}
