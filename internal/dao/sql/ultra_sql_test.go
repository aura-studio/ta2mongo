package sql

// Ultra-test coverage for doc/ultra_test.md §5.7 (SQL Data API), IDs SQL-2,
// SQL-4, SQL-5 and SQL-7, asserting the actual behavior of sql.go (New/Exec
// over the injected mongosql driver) and result.go (Result.MarshalEJSON).
//
// SQL-6 (DocumentDB rejecting pipeline-form updates like SET n = n + 1) is
// intentionally NOT covered here: the test stack runs mongo:6, which happily
// accepts aggregation-pipeline updates, so DocumentDB's rejection cannot be
// reproduced — it needs a real DocumentDB endpoint (see TestSQL6 skip below).
//
// The dao-side half of SQL-2 ((*Dao).SQL caching the construction error via
// sync.Once) lives in internal/dao/ultra_importboundary_test.go, package dao.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
)

// lazyResource builds a MongoResource around a client that has never touched
// the network: mongo.Connect in driver v2 is lazy, and the address is
// unroutable with tiny timeouts, so any accidental IO fails loudly instead of
// silently succeeding.
func lazyResource(t *testing.T) *daomongo.MongoResource {
	t.Helper()
	client, err := mongo.Connect(options.Client().
		ApplyURI("mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=200&connectTimeoutMS=200"))
	if err != nil {
		t.Fatalf("lazy connect: %v", err)
	}
	t.Cleanup(func() { _ = client.Disconnect(context.Background()) })
	return &daomongo.MongoResource{Client: client, DB: client.Database("sql_ultra_lazy")}
}

// connectSQLUltra opens the test connection pointed at a throwaway database
// (sql_test.go pattern: repoint res.DB before New) or skips without the URI.
func connectSQLUltra(t *testing.T, dbName string) *daomongo.MongoResource {
	t.Helper()
	uri := os.Getenv("TANGO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set TANGO_TEST_MONGO_URI to run the sql ultra integration tests")
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
	res.DB = res.Client.Database(dbName)
	t.Cleanup(func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = res.Client.Database(dbName).Drop(dctx)
		_ = res.Close()
	})
	return res
}

// asInt64 normalizes the numeric types relaxed EJSON round-trips can produce.
func asInt64(v any) int64 {
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

// TestSQL2_LazyInit_ErrorPathAndNoDial covers the sql-package half of SQL-2.
//
// Error path: New rejects a nil resource / nil DB with the exact, deterministic
// error "sql: nil MongoDB resource" (sql.go). This is the very error value
// (*Dao).SQL stores into d.sqlErr under d.sqlOnce (internal/dao/dao.go) and
// hands back verbatim on every later call — determinism here is what makes the
// dao-side caching coherent.
//
// No-dial path: New over a healthy resource must only build the mongosql
// translator (mongosql.New(db) -> translator.New(), zero IO) — dao owns the
// connection lifecycle, the driver never dials or pings. The client below
// points at an unroutable address with 200ms timeouts, so if New ever started
// dialing/pinging it would return an error instead of succeeding.
func TestSQL2_LazyInit_ErrorPathAndNoDial(t *testing.T) {
	const wantErr = "sql: nil MongoDB resource"
	for i := 0; i < 3; i++ {
		if _, err := New(nil); err == nil || err.Error() != wantErr {
			t.Fatalf("New(nil) call %d: want %q, got %v", i, wantErr, err)
		}
		if _, err := New(&daomongo.MongoResource{}); err == nil || err.Error() != wantErr {
			t.Fatalf("New(nil DB) call %d: want %q, got %v", i, wantErr, err)
		}
	}

	res := lazyResource(t)
	d, err := New(res)
	if err != nil {
		t.Fatalf("New over a never-dialed client: %v — New must not perform IO", err)
	}
	if d == nil {
		t.Fatal("New over a never-dialed client: nil driver")
	}
}

// TestSQL7_ParseAndUnsupportedErrors_Transparent covers SQL-7: parse failures
// and unsupported statements surface the underlying mongosql/vitess error
// text untouched — Driver.Exec (sql.go) returns e.d.Exec's error as-is. Both
// errors are produced by the translator before any server IO, so the
// never-dialed lazy client proves transparency without Mongo.
func TestSQL7_ParseAndUnsupportedErrors_Transparent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := New(lazyResource(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Garbage SQL -> mongosql wraps the vitess error as "parse SQL: %w";
	// vitess itself reports "syntax error at position N".
	_, err = d.Exec(ctx, "THIS IS NOT SQL AT ALL")
	if err == nil {
		t.Fatal("Exec(garbage): want parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse SQL") {
		t.Fatalf("parse error must pass through mongosql's \"parse SQL: ...\" wrap, got %q", err)
	}
	if !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("parse error must carry vitess's \"syntax error ...\" text, got %q", err)
	}

	// Parseable but unsupported statement (DDL) -> translator's
	// "unsupported statement type: %T" passes through.
	_, err = d.Exec(ctx, "DROP TABLE items")
	if err == nil {
		t.Fatal("Exec(DROP TABLE): want unsupported-statement error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported statement type") {
		t.Fatalf("unsupported statement must pass through translator text, got %q", err)
	}
}

// TestSQL4_SelectRows_MarshalEJSON_Integration covers SQL-4: a SELECT result
// EJSON-encodes (result.go MarshalEJSON, relaxed mode) into parseable JSON
// whose rows form a JSON array, with native BSON types preserved — ObjectID as
// {"$oid": ...}, datetime as {"$date": ...} — and round-trippable back into
// the same BSON values.
//
// Note on _id: mongosql's buildFindProjection hides MongoDB's auto _id for
// SELECT * (projection {"_id": 0} — "SQL clients expect only the columns they
// declared"), and includes it only when explicitly selected. Both contracts
// are asserted below.
func TestSQL4_SelectRows_MarshalEJSON_Integration(t *testing.T) {
	dbName := fmt.Sprintf("tango_sqlultra4_%d", time.Now().UnixNano())
	res := connectSQLUltra(t, dbName)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oid := bson.NewObjectID()
	when := bson.NewDateTimeFromTime(time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC))
	// Insert via the raw driver so the row carries real BSON types (SQL INSERT
	// literals could not produce an ObjectID/DateTime).
	if _, err := res.DB.Collection("typed").InsertOne(ctx,
		bson.M{"_id": oid, "name": "row1", "n": int64(42), "t": when}); err != nil {
		t.Fatalf("seed insert: %v", err)
	}

	d, err := New(res)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r, err := d.Exec(ctx, "SELECT * FROM typed")
	if err != nil {
		t.Fatalf("SELECT: %v", err)
	}
	if r.Kind != "select" || r.Rows == nil || len(*r.Rows) != 1 {
		t.Fatalf("SELECT result: want kind=select with 1 row, got %+v", r)
	}
	// SELECT * hides the auto _id (mongosql buildFindProjection: {"_id": 0}).
	if got, present := (*r.Rows)[0]["_id"]; present {
		t.Fatalf("SELECT *: _id must be projected out, got %v", got)
	}

	b, err := r.MarshalEJSON()
	if err != nil {
		t.Fatalf("MarshalEJSON: %v", err)
	}

	// 1) The bytes are plain-parseable JSON and rows is a JSON array.
	var plain struct {
		Kind string           `json:"kind"`
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(b, &plain); err != nil {
		t.Fatalf("output is not valid JSON: %v (bytes=%s)", err, b)
	}
	if plain.Kind != "select" || len(plain.Rows) != 1 {
		t.Fatalf("JSON shape: want kind=select rows[1], got %+v (bytes=%s)", plain, b)
	}
	// Relaxed EJSON renders the datetime as a {"$date": ...} wrapper.
	if _, ok := plain.Rows[0]["t"].(map[string]any); !ok {
		t.Fatalf("t: want a {\"$date\": ...} wrapper, got %v", plain.Rows[0]["t"])
	}

	// 2) The EJSON round-trips back to native BSON values.
	var back struct {
		Rows []bson.M `bson:"rows"`
	}
	if err := bson.UnmarshalExtJSON(b, false, &back); err != nil {
		t.Fatalf("EJSON re-decode: %v (bytes=%s)", err, b)
	}
	row := back.Rows[0]
	if got, ok := row["t"].(bson.DateTime); !ok || got != when {
		t.Fatalf("t round-trip: want %v, got %T %v", when, row["t"], row["t"])
	}
	if got := asInt64(row["n"]); got != 42 {
		t.Fatalf("n round-trip: want 42, got %T %v", row["n"], row["n"])
	}
	if row["name"] != "row1" {
		t.Fatalf("name round-trip: want row1, got %v", row["name"])
	}

	// 3) Explicitly selecting _id includes it, and it EJSON-encodes as $oid.
	r2, err := d.Exec(ctx, "SELECT _id, name FROM typed")
	if err != nil {
		t.Fatalf("SELECT _id: %v", err)
	}
	b2, err := r2.MarshalEJSON()
	if err != nil {
		t.Fatalf("MarshalEJSON(_id): %v", err)
	}
	var plain2 struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(b2, &plain2); err != nil {
		t.Fatalf("output is not valid JSON: %v (bytes=%s)", err, b2)
	}
	if len(plain2.Rows) != 1 {
		t.Fatalf("SELECT _id: want 1 row, got %+v (bytes=%s)", plain2, b2)
	}
	oidWrap, ok := plain2.Rows[0]["_id"].(map[string]any)
	if !ok || oidWrap["$oid"] != oid.Hex() {
		t.Fatalf("_id: want {\"$oid\": %q}, got %v", oid.Hex(), plain2.Rows[0]["_id"])
	}
}

// TestSQL5_TableIsCollection_DBFromURI_Integration covers SQL-5: the SQL table
// name maps 1:1 to the MongoDB collection name, and the database comes from
// the connection URI path (daomongo.MongoDBFromURI -> res.DB). Verified
// against the raw driver, not through SQL.
func TestSQL5_TableIsCollection_DBFromURI_Integration(t *testing.T) {
	uri := os.Getenv("TANGO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set TANGO_TEST_MONGO_URI to run the sql ultra integration tests")
	}
	dbName := fmt.Sprintf("tango_sqlultra5_%d", time.Now().UnixNano())
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatalf("parse TANGO_TEST_MONGO_URI: %v", err)
	}
	u.Path = "/" + dbName // URI now names the throwaway db in its path

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := daomongo.ConnectMongo(ctx, &daomongo.Config{
		URI:                    u.String(),
		ConnectTimeout:         10 * time.Second,
		ServerSelectionTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = res.Client.Database(dbName).Drop(dctx)
		_ = res.Close()
	}()

	// The database is resolved from the URI path, not configured separately.
	if got := res.DB.Name(); got != dbName {
		t.Fatalf("db from URI: want %q, got %q", dbName, got)
	}

	d, err := New(res)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO t (name, n) VALUES ('a', 1)`,
		`INSERT INTO t (name, n) VALUES ('b', 2)`,
	} {
		if _, err := d.Exec(ctx, q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	// Raw-driver proof: table "t" surfaced as collection "t" of the URI db.
	n, err := res.Client.Database(dbName).Collection("t").CountDocuments(ctx, bson.M{})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("collection mapping: want 2 docs in %s.t, got %d", dbName, n)
	}
	names, err := res.Client.Database(dbName).ListCollectionNames(ctx, bson.M{})
	if err != nil {
		t.Fatalf("list collections: %v", err)
	}
	found := false
	for _, name := range names {
		if name == "t" {
			found = true
		}
	}
	if !found {
		t.Fatalf("collection mapping: %q not among collections %v of db %s", "t", names, dbName)
	}
}

// TestSQL6_DocumentDBPipelineUpdate_Transparent is SQL-6 — intentionally
// skipped: it asserts that a pipeline-form UPDATE (e.g. `SET n = n + 1`, which
// mongosql translates into an aggregation-pipeline update) is REJECTED by
// Amazon DocumentDB and that the engine error passes through transparently.
// mongo:6 in the docker test stack supports pipeline updates and executes the
// statement successfully, so the failure mode cannot be reproduced here; it
// needs a real DocumentDB endpoint (see doc/ultra_test.md SQL-6 / EJ-11).
func TestSQL6_DocumentDBPipelineUpdate_Transparent(t *testing.T) {
	t.Skip("SQL-6 requires Amazon DocumentDB: mongo:6 accepts pipeline-form updates, so the DocumentDB rejection path cannot run here")
}
