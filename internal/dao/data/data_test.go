package data

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
)

// --- Unit tests (no database) ---------------------------------------------

// TestDecodeRequest_EJSONTypes verifies the request decoder preserves native
// BSON types written in canonical Extended JSON.
func TestDecodeRequest_EJSONTypes(t *testing.T) {
	in := []byte(`{
		"action": "find",
		"collection": "c",
		"filter": {
			"_id": {"$oid": "507f1f77bcf86cd799439011"},
			"t":   {"$date": "2024-01-02T03:04:05Z"},
			"n":   {"$numberLong": "9007199254740993"},
			"d":   {"$numberDecimal": "1.50"}
		}
	}`)
	req, err := DecodeRequest(in)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.Action != "find" || req.Collection != "c" {
		t.Fatalf("shell mismatch: action=%q collection=%q", req.Action, req.Collection)
	}
	if _, ok := req.Filter["_id"].(bson.ObjectID); !ok {
		t.Errorf("_id: want bson.ObjectID, got %T", req.Filter["_id"])
	}
	if _, ok := req.Filter["t"].(bson.DateTime); !ok {
		t.Errorf("t: want bson.DateTime, got %T", req.Filter["t"])
	}
	if v, ok := req.Filter["n"].(int64); !ok || v != 9007199254740993 {
		t.Errorf("n: want int64 9007199254740993, got %T %v", req.Filter["n"], req.Filter["n"])
	}
	if _, ok := req.Filter["d"].(bson.Decimal128); !ok {
		t.Errorf("d: want bson.Decimal128, got %T", req.Filter["d"])
	}
}

// TestResponse_MarshalEJSON_RoundTrip verifies the response encoder emits valid
// Extended JSON that round-trips native types.
func TestResponse_MarshalEJSON_RoundTrip(t *testing.T) {
	oid := bson.NewObjectID()
	resp := &Response{Document: bson.M{
		"_id": oid,
		"t":   bson.NewDateTimeFromTime(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)),
	}}
	b, err := resp.MarshalEJSON()
	if err != nil {
		t.Fatalf("MarshalEJSON: %v", err)
	}
	// Re-parse to confirm the bytes are valid EJSON and the ObjectID survived.
	var back struct {
		Document bson.M `bson:"document"`
	}
	if err := bson.UnmarshalExtJSON(b, false, &back); err != nil {
		t.Fatalf("re-decode: %v (bytes=%s)", err, b)
	}
	got, ok := back.Document["_id"].(bson.ObjectID)
	if !ok || got != oid {
		t.Errorf("_id round-trip: want %v, got %T %v", oid, back.Document["_id"], back.Document["_id"])
	}
}

func TestExecute_Validation(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		req  *Request
	}{
		{"nil request", nil},
		{"empty action", &Request{Collection: "c"}},
		{"unknown action", &Request{Action: "drop", Collection: "c"}},
		{"missing collection", &Request{Action: ActionFind}},
		{"missing database", &Request{Action: ActionFind, Collection: "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// nil resource: validation must fail before any connection use.
			if _, err := Execute(ctx, nil, tc.req); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

// --- Integration test (real MongoDB / DocumentDB) -------------------------

// TestDataAPI_Integration exercises every action end-to-end. It is skipped
// unless TANGO_TEST_MONGO_URI is set (point it at MongoDB or Amazon DocumentDB,
// including its tls/retryWrites query params).
func TestDataAPI_Integration(t *testing.T) {
	uri := os.Getenv("TANGO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set TANGO_TEST_MONGO_URI to run the data integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := daomongo.ConnectMongo(ctx, &daomongo.Config{
		URI:                    uri,
		ConnectTimeout:         10 * time.Second,
		ServerSelectionTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer res.Close()

	dbName := fmt.Sprintf("tango_data_it_%d", time.Now().UnixNano())
	const coll = "items"
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = res.Client.Database(dbName).Drop(dctx)
	}()

	exec := func(t *testing.T, req *Request) *Response {
		t.Helper()
		req.Database = dbName
		req.Collection = coll
		resp, err := Execute(ctx, res, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Action, err)
		}
		return resp
	}

	// insertOne x2
	if r := exec(t, &Request{Action: ActionInsertOne, Document: bson.M{"name": "a", "n": int64(1)}}); r.InsertedID == nil {
		t.Fatal("insertOne a: nil InsertedID")
	}
	exec(t, &Request{Action: ActionInsertOne, Document: bson.M{"name": "b", "n": int64(2)}})

	// find (sorted)
	if r := exec(t, &Request{Action: ActionFind, Filter: bson.M{}, Sort: bson.D{{Key: "n", Value: 1}}}); len(r.Documents) != 2 {
		t.Fatalf("find: want 2 docs, got %d", len(r.Documents))
	}

	// findOne
	if r := exec(t, &Request{Action: ActionFindOne, Filter: bson.M{"name": "a"}}); r.Document["name"] != "a" {
		t.Fatalf("findOne: want name=a, got %v", r.Document)
	}

	// updateOne ($set)
	if r := exec(t, &Request{Action: ActionUpdateOne, Filter: bson.M{"name": "a"}, Update: bson.M{"$set": bson.M{"n": int64(10)}}}); r.MatchedCount == nil || *r.MatchedCount != 1 {
		t.Fatalf("updateOne: want matched 1, got %v", r.MatchedCount)
	}

	// updateOne upsert
	if r := exec(t, &Request{Action: ActionUpdateOne, Filter: bson.M{"name": "c"}, Update: bson.M{"$set": bson.M{"n": int64(3)}}, Upsert: true}); r.UpsertedID == nil {
		t.Fatalf("updateOne upsert: nil UpsertedID")
	}

	// aggregate ($group sum) -> 10 + 2 + 3 = 15
	r := exec(t, &Request{Action: ActionAggregate, Pipeline: bson.A{
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: nil}, {Key: "total", Value: bson.D{{Key: "$sum", Value: "$n"}}}}}},
	}})
	if len(r.Documents) != 1 || toInt64(r.Documents[0]["total"]) != 15 {
		t.Fatalf("aggregate: want total 15, got %v", r.Documents)
	}

	// deleteOne
	if r := exec(t, &Request{Action: ActionDeleteOne, Filter: bson.M{"name": "b"}}); r.DeletedCount == nil || *r.DeletedCount != 1 {
		t.Fatalf("deleteOne: want deleted 1, got %v", r.DeletedCount)
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return -1
	}
}
