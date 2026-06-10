package ejson

// Ultra-test coverage for doc/ultra_test.md §5.6 (Mongo Data API), IDs
// EJ-2..EJ-7, EJ-9 and EJ-10 — one focused subtest per ID, asserting the
// actual behavior of Execute/findOne/find/insertOne/updateOne/deleteOne/
// aggregate in ejson.go and the EJSON encoding in codec.go.
//
// Integration tests: skipped unless TANGO_TEST_MONGO_URI is set (the docker
// compose test stacks inject mongodb://mongo:27017). Each test works in
// throwaway databases that are dropped on cleanup, following the
// TestEJSON_Integration pattern in ejson_test.go.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
)

// connectUltra opens the shared test connection or skips.
func connectUltra(t *testing.T) *daomongo.MongoResource {
	t.Helper()
	uri := os.Getenv("TANGO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set TANGO_TEST_MONGO_URI to run the ejson ultra integration tests")
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
	t.Cleanup(func() { _ = res.Close() })
	return res
}

// dropDBOnCleanup registers a cleanup that drops the named throwaway database.
func dropDBOnCleanup(t *testing.T, res *daomongo.MongoResource, db string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = res.Client.Database(db).Drop(ctx)
	})
}

func TestEJSON_Ultra(t *testing.T) {
	res := connectUltra(t)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	dbName := fmt.Sprintf("tango_ejson_ultra_%d", time.Now().UnixNano())
	dropDBOnCleanup(t, res, dbName)

	// exec runs a request against res and fails the (sub)test on error.
	exec := func(t *testing.T, r *daomongo.MongoResource, req *Request) *Response {
		t.Helper()
		resp, err := Execute(ctx, r, req)
		if err != nil {
			t.Fatalf("Execute(%s): %v", req.Action, err)
		}
		return resp
	}
	insert := func(t *testing.T, coll string, docs ...bson.M) {
		t.Helper()
		for _, d := range docs {
			exec(t, res, &Request{Action: ActionInsertOne, Database: dbName, Collection: coll, Document: d})
		}
	}

	// EJ-2: findOne honors projection, sort (bson.D, ordered) and skip
	// (ejson.go findOne applies SetProjection/SetSort/SetSkip), and a no-match
	// returns an empty Response, not an error (mongo.ErrNoDocuments swallowed).
	t.Run("EJ2_findOne_projection_sort_skip", func(t *testing.T) {
		const coll = "ej2"
		insert(t, coll,
			bson.M{"name": "a", "n": int64(1), "secret": "s1"},
			bson.M{"name": "b", "n": int64(2), "secret": "s2"},
			bson.M{"name": "c", "n": int64(3), "secret": "s3"},
		)
		resp := exec(t, res, &Request{
			Action:     ActionFindOne,
			Database:   dbName,
			Collection: coll,
			Sort:       bson.D{{Key: "n", Value: -1}}, // c(3), b(2), a(1)
			Skip:       1,                             // -> exactly "b"
			Projection: bson.M{"_id": 0, "name": 1},   // -> only the "name" field
		})
		if resp.Document == nil {
			t.Fatal("findOne: nil Document, want the skipped+sorted match")
		}
		if got := resp.Document["name"]; got != "b" {
			t.Fatalf("sort+skip: want doc \"b\" (2nd in n-desc order), got %v", resp.Document)
		}
		if len(resp.Document) != 1 {
			t.Fatalf("projection leaked fields: want only {name}, got %v", resp.Document)
		}
		// No match -> empty Response (Document nil), nil error.
		empty := exec(t, res, &Request{Action: ActionFindOne, Database: dbName, Collection: coll,
			Filter: bson.M{"name": "no-such-doc"}})
		if empty.Document != nil {
			t.Fatalf("findOne no-match: want empty Response, got document %v", empty.Document)
		}
	})

	// EJ-3: find with no matches still yields "documents":[] — drain() always
	// allocates a non-nil empty slice behind the *[]bson.M pointer field, so the
	// marshaled EJSON carries an empty ARRAY, never null and never a missing key.
	t.Run("EJ3_find_noMatch_emptyDocumentsArray", func(t *testing.T) {
		const coll = "ej3"
		insert(t, coll, bson.M{"name": "present"})
		resp := exec(t, res, &Request{Action: ActionFind, Database: dbName, Collection: coll,
			Filter: bson.M{"name": "absent"}})
		if resp.Documents == nil {
			t.Fatal("find no-match: Documents pointer is nil, want non-nil empty slice")
		}
		if len(*resp.Documents) != 0 {
			t.Fatalf("find no-match: want 0 docs, got %v", *resp.Documents)
		}
		b, err := resp.MarshalEJSON()
		if err != nil {
			t.Fatalf("MarshalEJSON: %v", err)
		}
		if !strings.Contains(string(b), `"documents":[]`) {
			t.Fatalf("marshaled EJSON must contain \"documents\":[] (empty array, not null/missing), got %s", b)
		}
	})

	// EJ-4: insertOne with no document is rejected by insertOne() before any
	// driver call, with the exact error text from ejson.go.
	t.Run("EJ4_insertOne_missingDocument", func(t *testing.T) {
		_, err := Execute(ctx, res, &Request{Action: ActionInsertOne, Database: dbName, Collection: "ej4"})
		if err == nil {
			t.Fatal("insertOne without document: want error, got nil")
		}
		if got := err.Error(); got != "ejson: insertOne requires a document" {
			t.Fatalf("error text: want %q, got %q", "ejson: insertOne requires a document", got)
		}
	})

	// EJ-5: updateOne with no update is rejected with the exact ejson.go error;
	// upsert:true on a non-matching filter returns UpsertedID plus pointer count
	// fields that keep their zero (matched=0, modified=0 still serialized).
	t.Run("EJ5_updateOne_missingUpdate_and_upsert", func(t *testing.T) {
		const coll = "ej5"
		_, err := Execute(ctx, res, &Request{Action: ActionUpdateOne, Database: dbName, Collection: coll,
			Filter: bson.M{"name": "x"}})
		if err == nil {
			t.Fatal("updateOne without update: want error, got nil")
		}
		if got := err.Error(); got != "ejson: updateOne requires an update" {
			t.Fatalf("error text: want %q, got %q", "ejson: updateOne requires an update", got)
		}

		resp := exec(t, res, &Request{Action: ActionUpdateOne, Database: dbName, Collection: coll,
			Filter: bson.M{"name": "ghost"},
			Update: bson.M{"$set": bson.M{"n": int64(7)}},
			Upsert: true,
		})
		if resp.UpsertedID == nil {
			t.Fatal("upsert on non-matching filter: want UpsertedID, got nil")
		}
		if resp.MatchedCount == nil || *resp.MatchedCount != 0 {
			t.Fatalf("upsert: want MatchedCount ptr to 0, got %v", resp.MatchedCount)
		}
		if resp.ModifiedCount == nil || *resp.ModifiedCount != 0 {
			t.Fatalf("upsert: want ModifiedCount ptr to 0, got %v", resp.ModifiedCount)
		}
		// Pointer fields keep the zero on the wire (Response doc comment).
		b, err := resp.MarshalEJSON()
		if err != nil {
			t.Fatalf("MarshalEJSON: %v", err)
		}
		if !strings.Contains(string(b), `"matchedCount":0`) || !strings.Contains(string(b), `"modifiedCount":0`) {
			t.Fatalf("marshaled upsert response must keep zero counts, got %s", b)
		}
		// The upserted document actually landed.
		got := exec(t, res, &Request{Action: ActionFindOne, Database: dbName, Collection: coll,
			Filter: bson.M{"name": "ghost"}})
		if got.Document == nil || toInt64(got.Document["n"]) != 7 {
			t.Fatalf("upserted doc: want n=7, got %v", got.Document)
		}
	})

	// EJ-6: deleteOne returns DeletedCount — 1 when a document matched, and a
	// non-nil pointer to 0 when nothing matched (still serialized as 0).
	t.Run("EJ6_deleteOne_deletedCount", func(t *testing.T) {
		const coll = "ej6"
		insert(t, coll, bson.M{"name": "victim"})
		del := exec(t, res, &Request{Action: ActionDeleteOne, Database: dbName, Collection: coll,
			Filter: bson.M{"name": "victim"}})
		if del.DeletedCount == nil || *del.DeletedCount != 1 {
			t.Fatalf("deleteOne matched: want DeletedCount=1, got %v", del.DeletedCount)
		}
		again := exec(t, res, &Request{Action: ActionDeleteOne, Database: dbName, Collection: coll,
			Filter: bson.M{"name": "victim"}})
		if again.DeletedCount == nil || *again.DeletedCount != 0 {
			t.Fatalf("deleteOne no-match: want DeletedCount ptr to 0, got %v", again.DeletedCount)
		}
		b, err := again.MarshalEJSON()
		if err != nil {
			t.Fatalf("MarshalEJSON: %v", err)
		}
		if !strings.Contains(string(b), `"deletedCount":0`) {
			t.Fatalf("no-match delete must still serialize deletedCount:0, got %s", b)
		}
	})

	// EJ-7: aggregate with a nil pipeline — aggregate() in ejson.go substitutes
	// bson.A{} (an empty pipeline), and MongoDB runs an empty pipeline as a full
	// collection scan, so ALL documents come back (not an error, not zero docs).
	t.Run("EJ7_aggregate_nilPipeline_returnsAllDocs", func(t *testing.T) {
		const coll = "ej7"
		insert(t, coll, bson.M{"name": "p"}, bson.M{"name": "q"})
		resp := exec(t, res, &Request{Action: ActionAggregate, Database: dbName, Collection: coll,
			Pipeline: nil})
		if resp.Documents == nil {
			t.Fatal("aggregate nil pipeline: nil Documents")
		}
		if len(*resp.Documents) != 2 {
			t.Fatalf("aggregate nil pipeline: empty pipeline must return all 2 docs, got %d (%v)",
				len(*resp.Documents), *resp.Documents)
		}
	})

	// EJ-9: when the request omits database, Execute resolves it from
	// res.DB.Name() (the database named in the connection URI); an explicit
	// Request.Database overrides that default. Written via the default path,
	// read back via the explicit path, with cross-database isolation asserted.
	t.Run("EJ9_database_default_and_override", func(t *testing.T) {
		nano := time.Now().UnixNano()
		defaultDB := fmt.Sprintf("tango_ejson_ultra_def_%d", nano)
		otherDB := fmt.Sprintf("tango_ejson_ultra_ovr_%d", nano)
		dropDBOnCleanup(t, res, defaultDB)
		dropDBOnCleanup(t, res, otherDB)

		// A resource whose URI-default database is defaultDB (same client/pool,
		// same shape ConnectMongo produces after MongoDBFromURI resolution).
		resDef := &daomongo.MongoResource{Client: res.Client, DB: res.Client.Database(defaultDB)}

		// Write WITHOUT Request.Database -> must land in the URI-default db.
		exec(t, resDef, &Request{Action: ActionInsertOne, Collection: "ej9",
			Document: bson.M{"who": "default-db"}})

		// Raw-driver proof it landed in defaultDB.
		n, err := res.Client.Database(defaultDB).Collection("ej9").CountDocuments(ctx, bson.M{})
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 1 {
			t.Fatalf("default-db routing: want 1 doc in %s.ej9, got %d", defaultDB, n)
		}

		// Read it back through ANOTHER resource by explicit Request.Database
		// (override wins over that resource's own default db).
		got := exec(t, res, &Request{Action: ActionFindOne, Database: defaultDB, Collection: "ej9"})
		if got.Document == nil || got.Document["who"] != "default-db" {
			t.Fatalf("explicit database read-back: want who=default-db, got %v", got.Document)
		}

		// Isolation: explicit Database=otherDB on resDef must NOT see the doc,
		// proving the explicit field overrides the resource's default database.
		miss := exec(t, resDef, &Request{Action: ActionFind, Database: otherDB, Collection: "ej9"})
		if miss.Documents == nil || len(*miss.Documents) != 0 {
			t.Fatalf("override isolation: want 0 docs in %s.ej9, got %v", otherDB, miss.Documents)
		}
	})

	// EJ-10 / X-7 REGRESSION GUARD — DO NOT "FIX" THIS TEST BY ADDING LIMITS.
	// The Data API is contractually fully open (package ejson doc comment,
	// doc/ultra_test.md EJ-10): ANY database name, ANY collection name (dots
	// included) and ANY operators ($gt, $in, $set, ...) are forwarded to the
	// driver verbatim — no allowlist, no limit caps, no timeout caps. This test
	// exists precisely to FAIL if someone adds an allowlist or restriction
	// layer to Execute later.
	t.Run("EJ10_noLimits_arbitraryDbCollOperators", func(t *testing.T) {
		zzDB := fmt.Sprintf("zz_anything_db_%d", time.Now().UnixNano())
		const weirdColl = "weird.coll" // dot in the collection name must pass through
		dropDBOnCleanup(t, res, zzDB)

		for i, name := range []string{"x", "y", "z"} {
			exec(t, res, &Request{Action: ActionInsertOne, Database: zzDB, Collection: weirdColl,
				Document: bson.M{"name": name, "n": int64(i + 1)}})
		}

		// Arbitrary query operators: $gt + $in.
		found := exec(t, res, &Request{Action: ActionFind, Database: zzDB, Collection: weirdColl,
			Filter: bson.M{
				"n":    bson.M{"$gt": int64(1)},
				"name": bson.M{"$in": bson.A{"y", "z"}},
			},
			Sort: bson.D{{Key: "n", Value: 1}},
		})
		if found.Documents == nil || len(*found.Documents) != 2 {
			t.Fatalf("$gt+$in in %s.%s: want 2 docs, got %v", zzDB, weirdColl, found.Documents)
		}
		if (*found.Documents)[0]["name"] != "y" || (*found.Documents)[1]["name"] != "z" {
			t.Fatalf("$gt+$in: want [y z], got %v", *found.Documents)
		}

		// Arbitrary update operator: $set.
		upd := exec(t, res, &Request{Action: ActionUpdateOne, Database: zzDB, Collection: weirdColl,
			Filter: bson.M{"name": "z"},
			Update: bson.M{"$set": bson.M{"tag": "open"}},
		})
		if upd.MatchedCount == nil || *upd.MatchedCount != 1 {
			t.Fatalf("$set in %s.%s: want matched 1, got %v", zzDB, weirdColl, upd.MatchedCount)
		}
		check := exec(t, res, &Request{Action: ActionFindOne, Database: zzDB, Collection: weirdColl,
			Filter: bson.M{"tag": "open"}})
		if check.Document == nil || check.Document["name"] != "z" {
			t.Fatalf("$set read-back: want name=z tagged open, got %v", check.Document)
		}
	})
}
