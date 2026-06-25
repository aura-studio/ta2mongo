// Facade-contract test for the v1.6.1 RunBackfill face: the WithBackfill*
// options must drive a real backfill run (mock TA OpenAPI) through the engine,
// landing events in Mongo, with the public surface staying plain Go types.
// Same Mongo gating as the other facade tests (ultraPingMongo /
// TANGO_TEST_MONGO_URI, throwaway db dropped via an independent connection).
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	mopt "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mockTAServer serves the submit→poll→paginate trio for a fixed event page set.
func mockTAServer(t *testing.T, headers []string, pages [][][]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env := func(data any) {
			raw, _ := json.Marshal(data)
			body, _ := json.Marshal(map[string]any{"return_code": 0, "return_message": "ok", "data": json.RawMessage(raw)})
			_, _ = w.Write(body)
		}
		switch r.URL.Path {
		case "/open/submit-sql":
			env(map[string]any{"taskId": "t1"})
		case "/open/sql-task-info":
			env(map[string]any{
				"taskId": "t1", "status": "FINISHED", "progress": 100,
				"resultStat": map[string]any{"headers": headers, "pageCount": len(pages), "pageSize": 1000},
			})
		case "/open/sql-result-page":
			pid, _ := strconv.Atoi(r.URL.Query().Get("pageId"))
			env(map[string]any{"pageId": pid})
			_, _ = w.Write([]byte("\n"))
			if pid >= 0 && pid < len(pages) {
				for _, row := range pages[pid] {
					line, _ := json.Marshal(row)
					_, _ = w.Write(line)
					_, _ = w.Write([]byte("\n"))
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRunBackfillFacade(t *testing.T) {
	ultraPingMongo(t)

	dbName := fmt.Sprintf("tango_client_backfill_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
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

	headers := []string{"#type", "#event_name", "#time", "#uuid", "#account_id"}
	pages := [][][]any{
		{{"track", "login", "2026-05-01 00:00:01", "cf-0", "acc"}},
		{{"track", "pay", "2026-05-01 00:00:02", "cf-1", "acc"}},
	}
	srv := mockTAServer(t, headers, pages)

	c, err := New(
		WithDaoMongoURI(uri),
		WithBackfillAPIBaseURL(srv.URL),
		WithBackfillToken("tok"),
		WithBackfillProjectID(35),
		WithBackfillPartDateRange("2026-05-01", "2026-05-01"),
		WithBackfillPageSize(1000),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	res, err := c.RunBackfill(ctx)
	if err != nil {
		t.Fatalf("RunBackfill: %v", err)
	}
	if res.EventWrites != 2 {
		t.Errorf("event writes = %d, want 2", res.EventWrites)
	}
	if n := ultraCount(t, db, "event"); n != 2 {
		t.Errorf("event collection = %d, want 2", n)
	}
}
