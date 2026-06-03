package batch

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/dao"
	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const testMongoURI = "mongodb://localhost:27017"

// pingMongo checks if MongoDB is available with a short timeout.
func pingMongo(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	clientOpts := options.Client().
		ApplyURI(testMongoURI).
		SetServerSelectionTimeout(2 * time.Second).
		SetConnectTimeout(2 * time.Second)

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		t.Skipf("MongoDB not available at %s: %v", testMongoURI, err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("MongoDB not available at %s: %v", testMongoURI, err)
	}
	_ = client.Disconnect(ctx)
}

func testIngesterSetup(t *testing.T) (*Ingester, *mongo.Database, func()) {
	t.Helper()
	pingMongo(t)

	dbName := fmt.Sprintf("tango_ingest_test_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := testMongoURI + "/" + dbName

	cfg := config.Config{
		Dao: &dao.Config{
			Mongo: &daomongo.Config{URI: uri},
			Store: &store.Config{MaxElapsedTime: 5 * time.Second},
		},
	}

	ctx := context.Background()

	ig, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("create ingester: %v", err)
	}

	// Create indexes
	if err := ig.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// Get DB handle for verification
	client, _ := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	db := client.Database(dbName)

	cleanup := func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(dropCtx)
		_ = client.Disconnect(dropCtx)
		_ = ig.Close()
	}

	return ig, db, cleanup
}

// ---------------------------------------------------------------------------
// Single line ingestion
// ---------------------------------------------------------------------------

func TestIngest_TrackEvent(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 12:00:00","#uuid":"ingest-evt-1","#account_id":"acc_ingest_1","properties":{"ip":"1.2.3.4","browser":"Chrome"}}`
	if err := ig.Ingest(ctx, line); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	// Verify event was written
	var doc bson.M
	err := db.Collection("event").FindOne(ctx, bson.M{"#uuid": "ingest-evt-1"}).Decode(&doc)
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
	count, err := db.Collection("id_mapping").CountDocuments(ctx, bson.M{"#account_id": "acc_ingest_1"})
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
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	line := `{"#type":"user_set","#time":"2024-01-01 12:00:00","#uuid":"ingest-user-1","#account_id":"acc_user_1","properties":{"name":"Alice","age":30}}`
	if err := ig.Ingest(ctx, line); err != nil {
		t.Fatalf("Ingest user_set: %v", err)
	}

	// Verify user document
	var doc bson.M
	err := db.Collection("user").FindOne(ctx, bson.M{"name": "Alice"}).Decode(&doc)
	if err != nil {
		t.Fatalf("FindOne user: %v", err)
	}
	if doc["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", doc["name"])
	}
}

func TestIngest_UserDel(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	// First create user
	setLine := `{"#type":"user_set","#time":"2024-01-01","#uuid":"del-u1","#account_id":"acc_del_1","properties":{"name":"ToDelete"}}`
	if err := ig.Ingest(ctx, setLine); err != nil {
		t.Fatalf("Ingest user_set: %v", err)
	}

	// Verify user exists
	count, _ := db.Collection("user").CountDocuments(ctx, bson.M{"name": "ToDelete"})
	if count != 1 {
		t.Fatalf("expected 1 user before delete, got %d", count)
	}

	// Delete user
	delLine := `{"#type":"user_del","#time":"2024-01-02","#uuid":"del-u2","#account_id":"acc_del_1"}`
	if err := ig.Ingest(ctx, delLine); err != nil {
		t.Fatalf("Ingest user_del: %v", err)
	}

	// Verify user is deleted
	count, _ = db.Collection("user").CountDocuments(ctx, bson.M{"name": "ToDelete"})
	if count != 0 {
		t.Errorf("expected 0 users after delete, got %d", count)
	}
}

func TestIngest_InvalidLine_GoesToDeadLetter(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	err := ig.Ingest(ctx, "this is not json")
	if err == nil {
		t.Fatal("expected error for invalid line")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("expected parse error, got: %v", err)
	}

	// Verify it went to dead letter
	count, cerr := db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if cerr != nil {
		t.Fatalf("count dead_letter: %v", cerr)
	}
	if count != 1 {
		t.Errorf("expected 1 dead letter, got %d", count)
	}
}

func TestIngest_NotTAPayload_GoesToDeadLetter(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	err := ig.Ingest(ctx, `{"foo":"bar"}`)
	if err == nil {
		t.Fatal("expected error for non-TA payload")
	}

	count, _ := db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if count != 1 {
		t.Errorf("expected 1 dead letter, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Batch ingestion
// ---------------------------------------------------------------------------

func TestIngestBatch_MixedLines(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	lines := []string{
		`{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"batch-e1","#account_id":"batch_acc_1"}`,
		`{"#type":"track","#event_name":"click","#time":"2024-01-01","#uuid":"batch-e2","#account_id":"batch_acc_1"}`,
		`{"#type":"user_set","#time":"2024-01-01","#uuid":"batch-u1","#account_id":"batch_acc_1","properties":{"name":"Alice"}}`,
		"invalid line",
		`{"#type":"track","#event_name":"purchase","#time":"2024-01-01","#uuid":"batch-e3","#distinct_id":"batch_did_2","properties":{"amount":99.9}}`,
	}

	err := ig.IngestBatch(ctx, lines)
	if err != nil {
		t.Fatalf("IngestBatch: %v", err)
	}

	// Verify events: 3 events
	eventCount, err := db.Collection("event").CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count events: %v", err)
	}
	if eventCount != 3 {
		t.Errorf("expected 3 events, got %d", eventCount)
	}

	// Verify users: 1 user_set
	userCount, err := db.Collection("user").CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != 1 {
		t.Errorf("expected 1 user, got %d", userCount)
	}

	// Verify dead letters: 1 invalid line
	dlCount, err := db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count dead_letter: %v", err)
	}
	if dlCount != 1 {
		t.Errorf("expected 1 dead letter, got %d", dlCount)
	}
}

func TestIngestBatch_AllValid(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	lines := []string{
		`{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"all-valid-1","#account_id":"av1"}`,
		`{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"all-valid-2","#account_id":"av2"}`,
		`{"#type":"user_set","#time":"2024-01-01","#uuid":"all-valid-3","#account_id":"av3","properties":{"x":1}}`,
	}

	if err := ig.IngestBatch(ctx, lines); err != nil {
		t.Fatalf("IngestBatch: %v", err)
	}

	eventCount, _ := db.Collection("event").CountDocuments(ctx, bson.M{})
	userCount, _ := db.Collection("user").CountDocuments(ctx, bson.M{})
	dlCount, _ := db.Collection("dead_letter").CountDocuments(ctx, bson.M{})

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
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	lines := []string{
		"bad1",
		"bad2",
		`{"foo":"bar"}`,
	}

	if err := ig.IngestBatch(ctx, lines); err != nil {
		t.Fatalf("IngestBatch: %v", err)
	}

	dlCount, _ := db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if dlCount != 3 {
		t.Errorf("expected 3 dead letters, got %d", dlCount)
	}
}

func TestIngestBatch_Empty(t *testing.T) {
	ig, _, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	if err := ig.IngestBatch(ctx, nil); err != nil {
		t.Fatalf("IngestBatch nil: %v", err)
	}
	if err := ig.IngestBatch(ctx, []string{}); err != nil {
		t.Fatalf("IngestBatch empty: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Identity resolution through ingest
// ---------------------------------------------------------------------------

func TestIngest_IdentityResolution_SameAccount(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	// Ingest two events for the same account
	line1 := `{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"id-res-1","#account_id":"id_res_acc"}`
	line2 := `{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"id-res-2","#account_id":"id_res_acc"}`

	if err := ig.Ingest(ctx, line1); err != nil {
		t.Fatalf("Ingest 1: %v", err)
	}
	if err := ig.Ingest(ctx, line2); err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}

	// Both events should have the same #user_id
	var doc1, doc2 bson.M
	if err := db.Collection("event").FindOne(ctx, bson.M{"#uuid": "id-res-1"}).Decode(&doc1); err != nil {
		t.Fatalf("FindOne 1: %v", err)
	}
	if err := db.Collection("event").FindOne(ctx, bson.M{"#uuid": "id-res-2"}).Decode(&doc2); err != nil {
		t.Fatalf("FindOne 2: %v", err)
	}

	if doc1["#user_id"] != doc2["#user_id"] {
		t.Errorf("expected same #user_id for same account, got %v and %v", doc1["#user_id"], doc2["#user_id"])
	}
}

func TestIngest_IdentityResolution_AccountAndDistinct(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	// First event with distinct_id only
	line1 := `{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"ad-1","#distinct_id":"ad_did"}`
	if err := ig.Ingest(ctx, line1); err != nil {
		t.Fatalf("Ingest 1: %v", err)
	}

	// Second event with both: should bind account to distinct's user
	line2 := `{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"ad-2","#account_id":"ad_acc","#distinct_id":"ad_did"}`
	if err := ig.Ingest(ctx, line2); err != nil {
		t.Fatalf("Ingest 2: %v", err)
	}

	var doc1, doc2 bson.M
	db.Collection("event").FindOne(ctx, bson.M{"#uuid": "ad-1"}).Decode(&doc1)
	db.Collection("event").FindOne(ctx, bson.M{"#uuid": "ad-2"}).Decode(&doc2)

	if doc1["#user_id"] != doc2["#user_id"] {
		t.Errorf("expected same #user_id after binding, got %v and %v", doc1["#user_id"], doc2["#user_id"])
	}
}

// ---------------------------------------------------------------------------
// Envelope format through ingest
// ---------------------------------------------------------------------------

func TestIngest_EnvelopeFormat(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()

	inner := `{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"env-1","#account_id":"env_acc"}`
	line := `{"level":"info","msg":"` + strings.ReplaceAll(inner, `"`, `\"`) + `"}`

	if err := ig.Ingest(ctx, line); err != nil {
		t.Fatalf("Ingest envelope: %v", err)
	}

	count, _ := db.Collection("event").CountDocuments(ctx, bson.M{"#uuid": "env-1"})
	if count != 1 {
		t.Errorf("expected 1 event from envelope, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// All user operation types through ingest
// ---------------------------------------------------------------------------

func TestIngest_AllUserOperations(t *testing.T) {
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()
	acc := "all_ops_acc"

	// user_setOnce: first, creating the user doc via setOnce so it can be verified
	ig.Ingest(ctx, fmt.Sprintf(`{"#type":"user_setOnce","#time":"2024-01-01","#uuid":"ops-1","#account_id":"%s","properties":{"first_login":"2024-01-01"}}`, acc))

	// user_set: should not overwrite first_login because setOnce already set it
	ig.Ingest(ctx, fmt.Sprintf(`{"#type":"user_set","#time":"2024-01-01","#uuid":"ops-2","#account_id":"%s","properties":{"name":"Alice","level":1}}`, acc))

	// user_add
	ig.Ingest(ctx, fmt.Sprintf(`{"#type":"user_add","#time":"2024-01-01","#uuid":"ops-3","#account_id":"%s","properties":{"level":2}}`, acc))

	// user_append
	ig.Ingest(ctx, fmt.Sprintf(`{"#type":"user_append","#time":"2024-01-01","#uuid":"ops-4","#account_id":"%s","properties":{"tags":["vip"]}}`, acc))

	// user_uniq_append
	ig.Ingest(ctx, fmt.Sprintf(`{"#type":"user_uniq_append","#time":"2024-01-01","#uuid":"ops-5","#account_id":"%s","properties":{"badges":["gold"]}}`, acc))

	// Verify user document has all properties
	var doc bson.M
	err := db.Collection("user").FindOne(ctx, bson.M{"name": "Alice"}).Decode(&doc)
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
	ig, db, cleanup := testIngesterSetup(t)
	defer cleanup()

	ctx := context.Background()
	acc := "unset_acc"

	// Create user with a field
	if err := ig.Ingest(ctx, fmt.Sprintf(`{"#type":"user_set","#time":"2024-01-01","#uuid":"unset-1","#account_id":"%s","properties":{"name":"Alice","to_remove":"value"}}`, acc)); err != nil {
		t.Fatalf("Ingest user_set: %v", err)
	}

	// Unset the field
	time.Sleep(5 * time.Millisecond)
	if err := ig.Ingest(ctx, fmt.Sprintf(`{"#type":"user_unset","#time":"2024-01-02","#uuid":"unset-2","#account_id":"%s","properties":{"to_remove":true}}`, acc)); err != nil {
		t.Fatalf("Ingest user_unset: %v", err)
	}

	var doc bson.M
	if err := db.Collection("user").FindOne(ctx, bson.M{"name": "Alice"}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if _, ok := doc["to_remove"]; ok {
		t.Errorf("expected to_remove to be unset, got %v", doc["to_remove"])
	}
	if doc["name"] != "Alice" {
		t.Errorf("expected name=Alice to remain, got %v", doc["name"])
	}
}

