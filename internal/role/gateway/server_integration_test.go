package gateway

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/internal/dao"
	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role/api"
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

// harness drives a Server against a throwaway database.
type harness struct {
	t   *testing.T
	srv *Server
	db  *mongo.Database
}

// upload runs the given mode and fails the test on error.
func (h *harness) upload(mode process.Mode, lines ...string) api.Result {
	h.t.Helper()
	res, err := h.srv.Upload(context.Background(), mode, lines)
	if err != nil {
		h.t.Fatalf("upload(%s): %v", mode, err)
	}
	return res
}

func (h *harness) single(lines ...string) api.Result { return h.upload(process.ModeSingle, lines...) }
func (h *harness) batch(lines ...string) api.Result  { return h.upload(process.ModeBatch, lines...) }

func testServerSetup(t *testing.T) (*harness, func()) {
	t.Helper()
	pingMongo(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("tango_gw_test_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := testMongoURI + "/" + dbName

	srv, err := New(ctx, &dao.Config{Mongo: &daomongo.Config{URI: uri}}, nil, nil, Config{})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := srv.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	verifyClient, _ := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	db := verifyClient.Database(dbName)

	cleanup := func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(dropCtx)
		_ = verifyClient.Disconnect(dropCtx)
		_ = srv.Close()
	}

	return &harness{t: t, srv: srv, db: db}, cleanup
}

// ---------------------------------------------------------------------------
// Single mode
// ---------------------------------------------------------------------------

func TestServer_Single_Track(t *testing.T) {
	h, cleanup := testServerSetup(t)
	defer cleanup()

	ctx := context.Background()

	res := h.single(`{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"gw-evt-1","#account_id":"gw_acc_1","properties":{"device":"mobile"}}`)
	if res.EventWrites != 1 {
		t.Errorf("expected 1 event write, got %d", res.EventWrites)
	}

	var doc bson.M
	if err := h.db.Collection("event").FindOne(ctx, bson.M{"#uuid": "gw-evt-1"}).Decode(&doc); err != nil {
		t.Fatalf("FindOne: %v", err)
	}
	if doc["#event_name"] != "login" {
		t.Errorf("expected event_name=login, got %v", doc["#event_name"])
	}
	if doc["device"] != "mobile" {
		t.Errorf("expected device=mobile, got %v", doc["device"])
	}
}

func TestServer_Single_UserSet(t *testing.T) {
	h, cleanup := testServerSetup(t)
	defer cleanup()

	ctx := context.Background()

	h.single(`{"#type":"user_set","#time":"2024-01-01","#uuid":"gw-u-1","#account_id":"gw_acc_2","properties":{"name":"GwUser","vip":true}}`)

	var doc bson.M
	if err := h.db.Collection("user").FindOne(ctx, bson.M{"name": "GwUser"}).Decode(&doc); err != nil {
		t.Fatalf("FindOne user: %v", err)
	}
	if doc["vip"] != true {
		t.Errorf("expected vip=true, got %v", doc["vip"])
	}
}

func TestServer_Single_InvalidLine(t *testing.T) {
	h, cleanup := testServerSetup(t)
	defer cleanup()

	ctx := context.Background()

	// Per-line parse failures are routed to dead_letter, not returned as errors.
	res := h.single("not valid json")
	if res.DeadLetters != 1 {
		t.Errorf("expected 1 dead letter in result, got %d", res.DeadLetters)
	}

	count, _ := h.db.Collection("dead_letter").CountDocuments(ctx, bson.M{})
	if count != 1 {
		t.Errorf("expected 1 dead letter, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Batch mode
// ---------------------------------------------------------------------------

func TestServer_Batch_Mixed(t *testing.T) {
	h, cleanup := testServerSetup(t)
	defer cleanup()

	ctx := context.Background()

	h.batch(
		`{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"gw-batch-1","#account_id":"gb1"}`,
		`{"#type":"user_set","#time":"2024-01-01","#uuid":"gw-batch-2","#account_id":"gb2","properties":{"x":1}}`,
		"invalid line",
		`{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"gw-batch-3","#distinct_id":"gb_did"}`,
	)

	eventCount, _ := h.db.Collection("event").CountDocuments(ctx, bson.M{})
	userCount, _ := h.db.Collection("user").CountDocuments(ctx, bson.M{})
	dlCount, _ := h.db.Collection("dead_letter").CountDocuments(ctx, bson.M{})

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

func TestServer_Batch_Large(t *testing.T) {
	h, cleanup := testServerSetup(t)
	defer cleanup()

	ctx := context.Background()

	n := 100
	lines := make([]string, n)
	for i := 0; i < n; i++ {
		lines[i] = fmt.Sprintf(`{"#type":"track","#event_name":"bulk_test","#time":"2024-01-01","#uuid":"bulk-%d","#account_id":"bulk_acc_%d"}`, i, i%10)
	}

	res := h.batch(lines...)
	if res.EventWrites != int64(n) {
		t.Errorf("expected %d event writes in result, got %d", n, res.EventWrites)
	}

	count, _ := h.db.Collection("event").CountDocuments(ctx, bson.M{"#event_name": "bulk_test"})
	if count != int64(n) {
		t.Errorf("expected %d events, got %d", n, count)
	}
}

// ---------------------------------------------------------------------------
// Pipeline mode
// ---------------------------------------------------------------------------

func TestServer_Pipeline_Track(t *testing.T) {
	h, cleanup := testServerSetup(t)
	defer cleanup()

	ctx := context.Background()

	h.upload(process.ModePipeline,
		`{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"gw-pipe-1","#account_id":"gp1"}`,
		`{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"gw-pipe-2","#account_id":"gp1"}`,
	)

	count, _ := h.db.Collection("event").CountDocuments(ctx, bson.M{})
	if count != 2 {
		t.Errorf("expected 2 events, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Index + end-to-end
// ---------------------------------------------------------------------------

func TestServer_EnsureIndexes_Idempotent(t *testing.T) {
	h, cleanup := testServerSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := h.srv.EnsureIndexes(ctx); err != nil {
		t.Fatalf("second EnsureIndexes: %v", err)
	}
	if err := h.srv.EnsureIndexes(ctx); err != nil {
		t.Fatalf("third EnsureIndexes: %v", err)
	}
}

func TestServer_EndToEnd_FullFlow(t *testing.T) {
	h, cleanup := testServerSetup(t)
	defer cleanup()

	ctx := context.Background()

	// Applied in order via single mode (per-line immediate writes).
	h.single(`{"#type":"user_setOnce","#time":"2024-01-01","#uuid":"e2e-1","#account_id":"e2e_acc","properties":{"signup_date":"2024-01-01"}}`)
	h.single(`{"#type":"user_set","#time":"2024-01-01","#uuid":"e2e-2","#account_id":"e2e_acc","properties":{"name":"E2E User","level":1}}`)
	h.single(`{"#type":"track","#event_name":"login","#time":"2024-01-01 12:00:00","#uuid":"e2e-3","#account_id":"e2e_acc","properties":{"ip":"1.2.3.4"}}`)
	h.single(`{"#type":"track","#event_name":"purchase","#time":"2024-01-01 13:00:00","#uuid":"e2e-4","#account_id":"e2e_acc","properties":{"amount":99.9}}`)
	h.single(`{"#type":"user_add","#time":"2024-01-01","#uuid":"e2e-5","#account_id":"e2e_acc","properties":{"level":1}}`)
	h.single(`{"#type":"user_append","#time":"2024-01-01","#uuid":"e2e-6","#account_id":"e2e_acc","properties":{"tags":["vip"]}}`)

	var userDoc bson.M
	if err := h.db.Collection("user").FindOne(ctx, bson.M{"name": "E2E User"}).Decode(&userDoc); err != nil {
		t.Fatalf("FindOne user: %v", err)
	}
	if userDoc["signup_date"] != "2024-01-01" {
		t.Errorf("expected signup_date, got %v", userDoc["signup_date"])
	}

	eventCount, _ := h.db.Collection("event").CountDocuments(ctx, bson.M{})
	if eventCount != 2 {
		t.Errorf("expected 2 events, got %d", eventCount)
	}

	cursor, err := h.db.Collection("event").Find(ctx, bson.M{})
	if err != nil {
		t.Fatalf("Find events: %v", err)
	}
	var events []bson.M
	if err := cursor.All(ctx, &events); err != nil {
		t.Fatalf("cursor.All: %v", err)
	}
	if len(events) >= 2 && events[0]["#user_id"] != events[1]["#user_id"] {
		t.Errorf("expected same #user_id across events, got %v and %v", events[0]["#user_id"], events[1]["#user_id"])
	}
}
