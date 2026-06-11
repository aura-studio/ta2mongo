// Tests for the Data API query faces on the public client — EJSON / SQL. The
// faces relay to the same cores as gateway POST /ejson and /sql (api.Engine
// EJSON/SQL via the dao facade), so these tests pin the facade contract: the
// public surface is bytes-in/bytes-out (no internal types leak), a malformed
// request shell errors before touching the database, and an insertOne is
// observable through both the EJSON find and the SQL SELECT face.
//
// client.New verifies the MongoDB connection with a Ping, so even the
// no-database error paths need a reachable server; everything therefore lives
// in one integration test against TANGO_TEST_MONGO_URI (falling back to a local
// mongod, skipping when unreachable) on a throwaway database injected into the
// URI path and dropped on cleanup, mirroring config_facade_test.go.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	mopt "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// decodeJSON unmarshals a relaxed Extended JSON response — plain JSON to a
// reader — into a map for assertions.
func decodeJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("response is not valid JSON: %v (bytes=%s)", err, b)
	}
	return m
}

// TestQueryFacade_EJSONAndSQL drives insertOne → find → SQL SELECT → deleteOne
// through the public client against a real MongoDB, plus the bad-request error
// paths that reject before any database work.
func TestQueryFacade_EJSONAndSQL(t *testing.T) {
	ultraPingMongo(t)

	dbName := fmt.Sprintf("tango_client_query_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	uri := ultraWithDBName(ultraMongoURI(), dbName)

	// Independent verify connection; drops the throwaway db on cleanup.
	vc, err := mongo.Connect(mopt.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("verify connect: %v", err)
	}
	db := vc.Database(dbName)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(ctx)
		_ = vc.Disconnect(ctx)
	})

	c, err := New(WithDaoMongoURI(uri))
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()
	const coll = "query_items"

	// A request shell that is not valid Extended JSON fails in decode, before
	// any database round-trip.
	if _, err := c.EJSON(ctx, []byte(`{not json`)); err == nil {
		t.Error("EJSON accepted a malformed request shell")
	}
	// Likewise an unknown action is rejected by the shell validation.
	if _, err := c.EJSON(ctx, []byte(`{"action": "explode", "collection": "x"}`)); err == nil {
		t.Error("EJSON accepted an unknown action")
	}

	// insertOne: the response must carry the generated _id.
	ins, err := c.EJSON(ctx, []byte(fmt.Sprintf(
		`{"action": "insertOne", "collection": %q, "document": {"name": "alpha", "n": 42}}`, coll)))
	if err != nil {
		t.Fatalf("EJSON insertOne: %v", err)
	}
	if m := decodeJSON(t, ins); m["insertedId"] == nil {
		t.Fatalf("insertOne response lacks insertedId: %s", ins)
	}

	// find: the inserted document comes back through the EJSON face, with the
	// database defaulted from the connection URI.
	fnd, err := c.EJSON(ctx, []byte(fmt.Sprintf(
		`{"action": "find", "collection": %q, "filter": {"name": "alpha"}}`, coll)))
	if err != nil {
		t.Fatalf("EJSON find: %v", err)
	}
	docs, ok := decodeJSON(t, fnd)["documents"].([]any)
	if !ok || len(docs) != 1 {
		t.Fatalf("find documents = %s, want exactly the inserted one", fnd)
	}
	doc, ok := docs[0].(map[string]any)
	if !ok || doc["name"] != "alpha" || doc["n"] != float64(42) {
		t.Fatalf("find document = %v, want name=alpha n=42", docs[0])
	}

	// The same document through the SQL face: a SELECT result carries its rows.
	sel, err := c.SQL(ctx, fmt.Sprintf(`SELECT name, n FROM %s WHERE name = 'alpha'`, coll))
	if err != nil {
		t.Fatalf("SQL select: %v", err)
	}
	sm := decodeJSON(t, sel)
	if sm["kind"] != "select" {
		t.Fatalf("SQL result kind = %v, want select (bytes=%s)", sm["kind"], sel)
	}
	rows, ok := sm["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("SQL rows = %s, want exactly one", sel)
	}
	row, ok := rows[0].(map[string]any)
	if !ok || row["name"] != "alpha" || row["n"] != float64(42) {
		t.Fatalf("SQL row = %v, want name=alpha n=42", rows[0])
	}

	// A statement the SQL driver cannot parse surfaces as an error.
	if _, err := c.SQL(ctx, "THIS IS NOT SQL AT ALL"); err == nil {
		t.Error("SQL accepted an unparsable statement")
	}

	// deleteOne cleans the document up and reports it.
	del, err := c.EJSON(ctx, []byte(fmt.Sprintf(
		`{"action": "deleteOne", "collection": %q, "filter": {"name": "alpha"}}`, coll)))
	if err != nil {
		t.Fatalf("EJSON deleteOne: %v", err)
	}
	if m := decodeJSON(t, del); m["deletedCount"] != float64(1) {
		t.Fatalf("deleteOne response = %s, want deletedCount 1", del)
	}
	fnd, err = c.EJSON(ctx, []byte(fmt.Sprintf(
		`{"action": "find", "collection": %q, "filter": {"name": "alpha"}}`, coll)))
	if err != nil {
		t.Fatalf("EJSON find after delete: %v", err)
	}
	if docs, ok := decodeJSON(t, fnd)["documents"].([]any); !ok || len(docs) != 0 {
		t.Fatalf("find after delete = %s, want an empty documents array", fnd)
	}
}
