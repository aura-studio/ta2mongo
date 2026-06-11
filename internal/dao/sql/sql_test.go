package sql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
)

// TestNew_NilResource verifies the injection constructor rejects a missing
// connection without panicking.
func TestNew_NilResource(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil): expected error, got nil")
	}
	if _, err := New(&daomongo.MongoResource{}); err == nil {
		t.Fatal("New(empty): expected error, got nil")
	}
}

// TestSQL_Integration exercises a SELECT/INSERT/UPDATE/DELETE round-trip through
// the mongosql dependency. Skipped unless TANGO_TEST_MONGO_URI is set (MongoDB
// or Amazon DocumentDB). UPDATE uses a constant assignment so it stays a plain
// $set (DocumentDB rejects pipeline-form updates).
func TestSQL_Integration(t *testing.T) {
	uri := os.Getenv("TANGO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set TANGO_TEST_MONGO_URI to run the sql integration test")
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

	// Isolate to a throwaway database by pointing the resource at it before New.
	dbName := fmt.Sprintf("tango_sql_it_%d", time.Now().UnixNano())
	res.DB = res.Client.Database(dbName)
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = res.Client.Database(dbName).Drop(dctx)
	}()

	d, err := New(res)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	exec := func(q string) *Result {
		t.Helper()
		r, err := d.Exec(ctx, q)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		return r
	}

	if r := exec(`INSERT INTO items (name, n) VALUES ('a', 1)`); r.Kind != "insert" || len(r.InsertedIDs) != 1 {
		t.Fatalf("insert a: %+v", r)
	}
	exec(`INSERT INTO items (name, n) VALUES ('b', 2)`)

	if r := exec(`SELECT * FROM items`); r.Kind != "select" || r.Rows == nil || len(*r.Rows) != 2 {
		t.Fatalf("select all: want 2 rows, got %+v", r)
	}
	if r := exec(`SELECT name, n FROM items WHERE name = 'a'`); r.Rows == nil || len(*r.Rows) != 1 || (*r.Rows)[0]["name"] != "a" {
		t.Fatalf("select filtered: %+v", r)
	}
	if r := exec(`UPDATE items SET n = 10 WHERE name = 'a'`); r.Kind != "update" || r.MatchedCount == nil || *r.MatchedCount != 1 {
		t.Fatalf("update: %+v", r)
	}
	if r := exec(`DELETE FROM items WHERE name = 'b'`); r.Kind != "delete" || r.DeletedCount == nil || *r.DeletedCount != 1 {
		t.Fatalf("delete: %+v", r)
	}
	if r := exec(`SELECT * FROM items`); r.Rows == nil || len(*r.Rows) != 1 {
		t.Fatalf("select after: want 1 row, got %+v", r)
	}

	// MarshalEJSON sanity.
	if _, err := exec(`SELECT * FROM items`).MarshalEJSON(); err != nil {
		t.Fatalf("MarshalEJSON: %v", err)
	}

	// Empty-result shapes: the kind-owned pointer fields are set even when the
	// statement matches nothing, so the EJSON never collapses to {"kind": ...}.
	// A 0-row SELECT keeps "rows": [] ...
	zero := exec(`SELECT * FROM items WHERE name = 'nope'`)
	if zero.Rows == nil || len(*zero.Rows) != 0 {
		t.Fatalf("empty select: want non-nil empty rows, got %+v", zero)
	}
	if b, err := zero.MarshalEJSON(); err != nil || !strings.Contains(string(b), `"rows":[]`) {
		t.Fatalf("empty select EJSON: want \"rows\":[] present, got %s (err=%v)", b, err)
	}
	// ... an UPDATE that matched nothing keeps its zero counts ...
	upd := exec(`UPDATE items SET n = 99 WHERE name = 'nope'`)
	if upd.MatchedCount == nil || *upd.MatchedCount != 0 || upd.ModifiedCount == nil || *upd.ModifiedCount != 0 {
		t.Fatalf("no-match update: want zero counts set, got %+v", upd)
	}
	if b, err := upd.MarshalEJSON(); err != nil || !strings.Contains(string(b), `"matchedCount":0`) {
		t.Fatalf("no-match update EJSON: want \"matchedCount\":0 present, got %s (err=%v)", b, err)
	}
	// ... and so does a DELETE.
	del := exec(`DELETE FROM items WHERE name = 'nope'`)
	if del.DeletedCount == nil || *del.DeletedCount != 0 {
		t.Fatalf("no-match delete: want zero count set, got %+v", del)
	}
	if b, err := del.MarshalEJSON(); err != nil || !strings.Contains(string(b), `"deletedCount":0`) {
		t.Fatalf("no-match delete EJSON: want \"deletedCount\":0 present, got %s (err=%v)", b, err)
	}
}
