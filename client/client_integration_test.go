package client

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

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

func testClientSetup(t *testing.T) (*Client, *mongo.Database, func()) {
	t.Helper()
	pingMongo(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("tango_client_test_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := testMongoURI + "/" + dbName

	cli, err := New(ctx, WithURI(uri))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	if err := cli.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// Get DB for verification
	verifyClient, _ := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	db := verifyClient.Database(dbName)

	cleanup := func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(dropCtx)
		_ = verifyClient.Disconnect(dropCtx)
		_ = cli.Close()
	}

	return cli, db, cleanup
}

// ---------------------------------------------------------------------------
// Client creation
// ---------------------------------------------------------------------------

func TestNew_MissingURI(t *testing.T) {
	ctx := context.Background()
	_, err := New(ctx)
	if err == nil {
		t.Fatal("expected error for missing URI")
	}
	if !strings.Contains(err.Error(), "URI is required") {
		t.Errorf("expected 'URI is required' error, got: %v", err)
	}
}

func TestNew_WithOptions(t *testing.T) {
	pingMongo(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("tango_opt_test_%d", time.Now().UnixNano())
	uri := testMongoURI + "/" + dbName

	cli, err := New(ctx,
		WithURI(uri),
		WithMaxElapsedTime(30*time.Second),
		WithBatchSize(500),
	)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	defer func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, _ := mongo.Connect(dropCtx, options.Client().ApplyURI(uri))
		_ = c.Database(dbName).Drop(dropCtx)
		_ = c.Disconnect(dropCtx)
		_ = cli.Close()
	}()

	if cli.opts.MaxElapsedTime != 30*time.Second {
		t.Errorf("expected MaxElapsedTime=30s, got %v", cli.opts.MaxElapsedTime)
	}
	if cli.opts.BatchSize != 500 {
		t.Errorf("expected BatchSize=500, got %d", cli.opts.BatchSize)
	}
}

// ---------------------------------------------------------------------------
// Ping
// ---------------------------------------------------------------------------

func TestClient_Ping(t *testing.T) {
	cli, _, cleanup := testClientSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := cli.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

func TestClient_Stats(t *testing.T) {
	cli, _, cleanup := testClientSetup(t)
	defer cleanup()

	stats := cli.Stats()
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stats.TotalRetries() != 0 {
		t.Errorf("expected 0 retries, got %d", stats.TotalRetries())
	}
}

// ---------------------------------------------------------------------------
// Ingest single line
// ---------------------------------------------------------------------------

func TestClient_Ingest_Track(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()

	ctx := context.Background()

	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"cli-evt-1","#account_id":"cli_acc_1","properties":{"device":"mobile"}}`
	if err := cli.Ingest(ctx, line); err != nil {
		t.Fatalf("Ingest: %v", err)
	}

	var doc bson.M
	err := db.Collection("event").FindOne(ctx, bson.M{"#uuid": "cli-evt-1"}).Decode(&doc)
	if err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["#event_name"] != "login" {
		t.Errorf("expected event_name=login, got %v", doc["#event_name"])
	}
	if doc["device"] != "mobile" {
		t.Errorf("expected device=mobile, got %v", doc["device"])
	}
}

func TestClient_Ingest_UserSet(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()

	ctx := context.Background()

	line := `{"#type":"user_set","#time":"2024-01-01","#uuid":"cli-u-1","#account_id":"cli_acc_2","properties":{"name":"ClientUser","vip":true}}`
	if err := cli.Ingest(ctx, line); err != nil {
		t.Fatalf("Ingest user_set: %v", err)
	}

	var doc bson.M
	err := db.Collection("user").FindOne(ctx, bson.M{"name": "ClientUser"}).Decode(&doc)
	if err != nil {
		t.Fatalf("FindOne user: %v", err)
	}
	if doc["vip"] != true {
		t.Errorf("expected vip=true, got %v", doc["vip"])
	}
}

func TestClient_Ingest_InvalidLine(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()

	ctx := context.Background()

	err := cli.Ingest(ctx, "not valid json")
	if err == nil {
		t.Fatal("expected error for invalid line")
	}

	// Should go to dead letter
	count, _ := db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if count != 1 {
		t.Errorf("expected 1 dead letter, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// IngestBatch
// ---------------------------------------------------------------------------

func TestClient_IngestBatch_Mixed(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()

	ctx := context.Background()

	lines := []string{
		`{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"cli-batch-1","#account_id":"cb1"}`,
		`{"#type":"user_set","#time":"2024-01-01","#uuid":"cli-batch-2","#account_id":"cb2","properties":{"x":1}}`,
		"invalid line",
		`{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"cli-batch-3","#distinct_id":"cb_did"}`,
	}

	if err := cli.IngestBatch(ctx, lines); err != nil {
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
	if dlCount != 1 {
		t.Errorf("expected 1 dead letter, got %d", dlCount)
	}
}

func TestClient_IngestBatch_LargeBatch(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()

	ctx := context.Background()

	n := 100
	lines := make([]string, n)
	for i := 0; i < n; i++ {
		lines[i] = fmt.Sprintf(`{"#type":"track","#event_name":"bulk_test","#time":"2024-01-01","#uuid":"bulk-%d","#account_id":"bulk_acc_%d"}`, i, i%10)
	}

	if err := cli.IngestBatch(ctx, lines); err != nil {
		t.Fatalf("IngestBatch large: %v", err)
	}

	count, _ := db.Collection("event").CountDocuments(ctx, bson.M{"#event_name": "bulk_test"})
	if count != int64(n) {
		t.Errorf("expected %d events, got %d", n, count)
	}
}

// ---------------------------------------------------------------------------
// EnsureIndexes
// ---------------------------------------------------------------------------

func TestClient_EnsureIndexes_Idempotent(t *testing.T) {
	cli, _, cleanup := testClientSetup(t)
	defer cleanup()

	ctx := context.Background()

	// Already called in setup, call again
	if err := cli.EnsureIndexes(ctx); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}
	if err := cli.EnsureIndexes(ctx); err != nil {
		t.Fatalf("third EnsureIndexes: %v", err)
	}
}

// ---------------------------------------------------------------------------
// End-to-end: full flow
// ---------------------------------------------------------------------------

func TestClient_EndToEnd_FullFlow(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()

	ctx := context.Background()

	// 1. Set initial properties (user_setOnce first to create doc with these fields)
	cli.Ingest(ctx, `{"#type":"user_setOnce","#time":"2024-01-01","#uuid":"e2e-1","#account_id":"e2e_acc","properties":{"signup_date":"2024-01-01"}}`)

	// 2. Create user profile (user_set overwrites name/level but not signup_date)
	cli.Ingest(ctx, `{"#type":"user_set","#time":"2024-01-01","#uuid":"e2e-2","#account_id":"e2e_acc","properties":{"name":"E2E User","level":1}}`)

	// 3. Track events
	cli.Ingest(ctx, `{"#type":"track","#event_name":"login","#time":"2024-01-01 12:00:00","#uuid":"e2e-3","#account_id":"e2e_acc","properties":{"ip":"1.2.3.4"}}`)
	cli.Ingest(ctx, `{"#type":"track","#event_name":"purchase","#time":"2024-01-01 13:00:00","#uuid":"e2e-4","#account_id":"e2e_acc","properties":{"amount":99.9}}`)

	// 4. Increment counter
	cli.Ingest(ctx, `{"#type":"user_add","#time":"2024-01-01","#uuid":"e2e-5","#account_id":"e2e_acc","properties":{"level":1}}`)

	// 5. Append tags
	cli.Ingest(ctx, `{"#type":"user_append","#time":"2024-01-01","#uuid":"e2e-6","#account_id":"e2e_acc","properties":{"tags":["vip"]}}`)

	// Verify
	var userDoc bson.M
	err := db.Collection("user").FindOne(ctx, bson.M{"name": "E2E User"}).Decode(&userDoc)
	if err != nil {
		t.Fatalf("FindOne user: %v", err)
	}
	if userDoc["name"] != "E2E User" {
		t.Errorf("expected name='E2E User', got %v", userDoc["name"])
	}
	if userDoc["signup_date"] != "2024-01-01" {
		t.Errorf("expected signup_date, got %v", userDoc["signup_date"])
	}

	eventCount, _ := db.Collection("event").CountDocuments(ctx, bson.M{})
	if eventCount != 2 {
		t.Errorf("expected 2 events, got %d", eventCount)
	}

	// Verify all events have the same #user_id
	cursor, err := db.Collection("event").Find(ctx, bson.M{})
	if err != nil {
		t.Fatalf("Find events: %v", err)
	}
	var events []bson.M
	if err := cursor.All(ctx, &events); err != nil {
		t.Fatalf("cursor.All: %v", err)
	}
	if len(events) >= 2 {
		if events[0]["#user_id"] != events[1]["#user_id"] {
			t.Errorf("expected same #user_id across events, got %v and %v", events[0]["#user_id"], events[1]["#user_id"])
		}
	}
}
