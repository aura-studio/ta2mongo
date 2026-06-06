package sql

import (
	"context"
	"fmt"
	"os"
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

// TestSQL_Integration exercises a SELECT/INSERT/UPDATE/DELETE round-trip end to
// end. Skipped unless TANGO_TEST_MONGO_URI is set (MongoDB or Amazon DocumentDB,
// including its tls/retryWrites query params). UPDATE uses a constant assignment
// so it stays a plain $set (DocumentDB rejects pipeline-form updates).
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

	d, err := New(res)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	dbName := fmt.Sprintf("tango_sql_it_%d", time.Now().UnixNano())
	d.UseDB(dbName)
	defer func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = res.Client.Database(dbName).Drop(dctx)
	}()

	exec := func(q string) *Result {
		t.Helper()
		r, err := d.Exec(ctx, q)
		if err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
		return r
	}

	// INSERT
	if r := exec(`INSERT INTO items (name, n) VALUES ('a', 1)`); r.Kind != "insert" || len(r.InsertedIDs) != 1 {
		t.Fatalf("insert a: %+v", r)
	}
	exec(`INSERT INTO items (name, n) VALUES ('b', 2)`)

	// SELECT (all)
	if r := exec(`SELECT * FROM items`); r.Kind != "select" || len(r.Rows) != 2 {
		t.Fatalf("select all: want 2 rows, got %+v", r)
	}

	// SELECT (filtered)
	if r := exec(`SELECT name, n FROM items WHERE name = 'a'`); len(r.Rows) != 1 || r.Rows[0]["name"] != "a" {
		t.Fatalf("select filtered: %+v", r)
	}

	// UPDATE (constant -> plain $set, DocumentDB-safe)
	if r := exec(`UPDATE items SET n = 10 WHERE name = 'a'`); r.Kind != "update" || r.MatchedCount != 1 {
		t.Fatalf("update: %+v", r)
	}

	// DELETE
	if r := exec(`DELETE FROM items WHERE name = 'b'`); r.Kind != "delete" || r.DeletedCount != 1 {
		t.Fatalf("delete: %+v", r)
	}

	// SELECT after mutations
	if r := exec(`SELECT * FROM items`); len(r.Rows) != 1 {
		t.Fatalf("select after: want 1 row, got %+v", r)
	}
}
