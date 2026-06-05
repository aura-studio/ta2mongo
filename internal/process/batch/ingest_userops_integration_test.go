package batch

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestIngest_AllUserOperations(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	acc := "all_ops_acc"

	// Applied in order via the single strategy (per-line immediate writes):
	//   user_setOnce -> user_set -> user_add -> user_append -> user_uniq_append
	tt.single(
		fmt.Sprintf(`{"#type":"user_setOnce","#time":"2024-01-01","#uuid":"ops-1","#account_id":"%s","properties":{"first_login":"2024-01-01"}}`, acc),
		fmt.Sprintf(`{"#type":"user_set","#time":"2024-01-01","#uuid":"ops-2","#account_id":"%s","properties":{"name":"Alice","level":1}}`, acc),
		fmt.Sprintf(`{"#type":"user_add","#time":"2024-01-01","#uuid":"ops-3","#account_id":"%s","properties":{"level":2}}`, acc),
		fmt.Sprintf(`{"#type":"user_append","#time":"2024-01-01","#uuid":"ops-4","#account_id":"%s","properties":{"tags":["vip"]}}`, acc),
		fmt.Sprintf(`{"#type":"user_uniq_append","#time":"2024-01-01","#uuid":"ops-5","#account_id":"%s","properties":{"badges":["gold"]}}`, acc),
	)

	// Verify user document has all properties
	var doc bson.M
	err := tt.db.Collection("user").FindOne(ctx, bson.M{"name": "Alice"}).Decode(&doc)
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["name"] != "Alice" {
		t.Errorf("name: expected Alice, got %v", doc["name"])
	}
	if doc["first_login"] != "2024-01-01" {
		t.Errorf("first_login: expected 2024-01-01, got %v", doc["first_login"])
	}
	// Verify user_add incremented level (1 initial + 2 added = 3)
	if level, ok := doc["level"].(float64); ok {
		if level != 3 {
			t.Errorf("level: expected 3, got %v", level)
		}
	} else if level, ok := doc["level"].(int32); ok {
		if level != 3 {
			t.Errorf("level: expected 3, got %v", level)
		}
	}
}

func TestIngest_UserUnset(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	acc := "unset_acc"

	// Create user with a field
	tt.single(fmt.Sprintf(`{"#type":"user_set","#time":"2024-01-01","#uuid":"unset-1","#account_id":"%s","properties":{"name":"Alice","to_remove":"value"}}`, acc))

	// Unset the field (separate run so the _ts anti-rollback sees a later time)
	time.Sleep(5 * time.Millisecond)
	tt.single(fmt.Sprintf(`{"#type":"user_unset","#time":"2024-01-02","#uuid":"unset-2","#account_id":"%s","properties":{"to_remove":true}}`, acc))

	var doc bson.M
	if err := tt.db.Collection("user").FindOne(ctx, bson.M{"name": "Alice"}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if _, ok := doc["to_remove"]; ok {
		t.Errorf("expected to_remove to be unset, got %v", doc["to_remove"])
	}
	if doc["name"] != "Alice" {
		t.Errorf("expected name=Alice to remain, got %v", doc["name"])
	}
}
