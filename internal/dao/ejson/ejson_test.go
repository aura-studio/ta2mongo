package ejson

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

// TestExecute_ListCollectionsNoCollectionRequired proves listCollections is
// database-level: a missing collection must NOT be the failure reason.
func TestExecute_ListCollectionsNoCollectionRequired(t *testing.T) {
	_, err := Execute(context.Background(), nil, &Request{Action: ActionListCollections, Database: "d"})
	if err == nil {
		t.Fatal("expected an error (nil resource)")
	}
	if err.Error() == "ejson: collection is required" {
		t.Fatalf("listCollections must not require a collection, got %v", err)
	}
}

// --- Integration test (real MongoDB / DocumentDB) -------------------------

// TestEJSON_Integration exercises every action end-to-end. It is skipped unless
// TANGO_TEST_MONGO_URI is set (point it at MongoDB or Amazon DocumentDB,
// including its tls/retryWrites query params).
func TestEJSON_Integration(t *testing.T) {
	uri := os.Getenv("TANGO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set TANGO_TEST_MONGO_URI to run the ejson integration test")
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

	dbName := fmt.Sprintf("tango_ejson_it_%d", time.Now().UnixNano())
	const coll = "items"
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = res.Client.Database(dbName).Drop(dctx)
	}()

	exec := func(t *testing.T, req *Request) *Response {
		t.Helper()
		req.Database = dbName
		if req.Collection == "" {
			req.Collection = coll
		}
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
	if r := exec(t, &Request{Action: ActionFind, Filter: bson.M{}, Sort: bson.D{{Key: "n", Value: 1}}}); r.Documents == nil || len(*r.Documents) != 2 {
		t.Fatalf("find: want 2 docs, got %v", r.Documents)
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
	if r.Documents == nil || len(*r.Documents) != 1 || toInt64((*r.Documents)[0]["total"]) != 15 {
		t.Fatalf("aggregate: want total 15, got %v", r.Documents)
	}

	// deleteOne
	if r := exec(t, &Request{Action: ActionDeleteOne, Filter: bson.M{"name": "b"}}); r.DeletedCount == nil || *r.DeletedCount != 1 {
		t.Fatalf("deleteOne: want deleted 1, got %v", r.DeletedCount)
	}

	// --- schema introspection & index management ---

	// createIndexes: a unique index on name
	if r := exec(t, &Request{Action: ActionCreateIndexes, Keys: bson.D{{Key: "name", Value: 1}}, IndexName: "name_1", Unique: true}); r.IndexName != "name_1" {
		t.Fatalf("createIndexes: want name_1, got %q", r.IndexName)
	}

	// listIndexes: name_1 present, unique, keyed on name
	{
		r := exec(t, &Request{Action: ActionListIndexes})
		if r.Documents == nil {
			t.Fatal("listIndexes: nil Documents")
		}
		var found bool
		for _, d := range *r.Documents {
			if d["name"] == "name_1" {
				found = true
				if u, _ := d["unique"].(bool); !u {
					t.Errorf("listIndexes: name_1 should be unique")
				}
			}
		}
		if !found {
			t.Fatalf("listIndexes: name_1 not found in %v", *r.Documents)
		}
	}

	// sampleFields: name + n discovered
	{
		r := exec(t, &Request{Action: ActionSampleFields})
		if r.Documents == nil {
			t.Fatal("sampleFields: nil Documents")
		}
		seen := map[string]bool{}
		for _, d := range *r.Documents {
			if f, ok := d["field"].(string); ok {
				seen[f] = true
			}
		}
		if !seen["name"] || !seen["n"] {
			t.Fatalf("sampleFields: want name+n, got %v", *r.Documents)
		}
	}

	// listCollections: items present (collection is ignored for this action)
	{
		r := exec(t, &Request{Action: ActionListCollections})
		if r.Collections == nil {
			t.Fatal("listCollections: nil Collections")
		}
		var found bool
		for _, name := range *r.Collections {
			if name == coll {
				found = true
			}
		}
		if !found {
			t.Fatalf("listCollections: %q not found in %v", coll, *r.Collections)
		}
	}

	// dropIndexes: remove name_1
	if r := exec(t, &Request{Action: ActionDropIndexes, IndexName: "name_1"}); r.IndexName != "name_1" {
		t.Fatalf("dropIndexes: want name_1, got %q", r.IndexName)
	}

	// createTable: a fresh collection appears in listCollections; dropTable removes it.
	const newTbl = "items_new"
	if r := exec(t, &Request{Action: ActionCreateTable, Collection: newTbl}); r.Collections == nil || len(*r.Collections) != 1 || (*r.Collections)[0] != newTbl {
		t.Fatalf("createTable: want [%s], got %v", newTbl, r.Collections)
	}
	{
		r := exec(t, &Request{Action: ActionListCollections})
		found := false
		for _, n := range *r.Collections {
			if n == newTbl {
				found = true
			}
		}
		if !found {
			t.Fatalf("createTable: %q not in listCollections %v", newTbl, *r.Collections)
		}
	}
	if r := exec(t, &Request{Action: ActionDropTable, Collection: newTbl}); r.Collections == nil || (*r.Collections)[0] != newTbl {
		t.Fatalf("dropTable: want [%s], got %v", newTbl, r.Collections)
	}
	{
		r := exec(t, &Request{Action: ActionListCollections})
		for _, n := range *r.Collections {
			if n == newTbl {
				t.Fatalf("dropTable: %q still present after drop", newTbl)
			}
		}
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
