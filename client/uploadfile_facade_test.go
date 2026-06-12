// Facade-contract test for the v1.6.0 UploadFile face: the client must bulk
// import the files matching its call-time glob patterns through the engine
// (the same parse → filter → identity-resolve → write path as Upload), reject
// a pattern-less call before any database work, and keep the public surface at
// plain Go types. Same conventions as query_facade_test.go: real Mongo gated
// by ultraPingMongo (TANGO_TEST_MONGO_URI), throwaway database injected into
// the URI path and dropped via an independent verify connection.
package client

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	mopt "go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestUploadFileFacade(t *testing.T) {
	ultraPingMongo(t)

	dbName := fmt.Sprintf("tango_client_uploadfile_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	uri := ultraWithDBName(ultraMongoURI(), dbName)

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
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// A pattern-less call must be rejected by the engine before any source or
	// database work.
	if _, err := c.UploadFile(ctx); err == nil || !strings.Contains(err.Error(), "logPattern") {
		t.Fatalf("UploadFile() without patterns: err = %v, want logPattern-required error", err)
	}

	// Two matched log files (4 track + 1 user_set + 1 dead letter) and one
	// unmatched file that must not import.
	dir := t.TempDir()
	writeTempLog(t, filepath.Join(dir, "a.log"),
		`{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"uf-e1","#account_id":"acc"}`,
		`{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"uf-e2","#account_id":"acc"}`,
		"this is not json",
	)
	writeTempLog(t, filepath.Join(dir, "b.log"),
		`{"#type":"track","#event_name":"e3","#time":"2024-01-01","#uuid":"uf-e3","#account_id":"acc"}`,
		`{"#type":"track","#event_name":"e4","#time":"2024-01-01","#uuid":"uf-e4","#account_id":"acc"}`,
		`{"#type":"user_set","#time":"2024-01-01","#uuid":"uf-u1","#account_id":"acc","properties":{"name":"Alice"}}`,
	)
	writeTempLog(t, filepath.Join(dir, "skip.txt"),
		`{"#type":"track","#event_name":"never","#time":"2024-01-01","#uuid":"uf-never","#account_id":"acc"}`,
	)

	res, err := c.UploadFile(ctx, filepath.Join(dir, "*.log"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if res.Lines != 6 || res.EventWrites != 4 || res.UserWrites != 1 || res.DeadLetters != 1 {
		t.Fatalf("UploadFile result = %+v, want lines=6 event=4 user=1 dead=1", res)
	}
	if n := ultraCount(t, db, "event"); n != 4 {
		t.Fatalf("event count = %d, want 4 (skip.txt must not import)", n)
	}
	if n := ultraCount(t, db, "user"); n != 1 {
		t.Fatalf("user count = %d, want 1", n)
	}

	// Re-import: no checkpoint means everything streams again (same line
	// count), but the idempotent write models keep the database unchanged.
	res2, err := c.UploadFile(ctx, filepath.Join(dir, "*.log"))
	if err != nil {
		t.Fatalf("UploadFile re-run: %v", err)
	}
	if res2.Lines != 6 {
		t.Fatalf("re-run lines = %d, want 6 (full re-read, no checkpoint)", res2.Lines)
	}
	if n := ultraCount(t, db, "event"); n != 4 {
		t.Fatalf("event count after re-import = %d, want 4 (uuid upsert idempotency)", n)
	}
	if n := ultraCount(t, db, "user"); n != 1 {
		t.Fatalf("user count after re-import = %d, want 1", n)
	}
}

func writeTempLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
