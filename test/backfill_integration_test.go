// Repo-level integration tests for the backfill domain: a mock ThinkingData
// OpenAPI (httptest, the submit→poll→paginate trio) feeding Engine.RunBackfill
// against a real MongoDB/DocumentDB temp database. Backfill now streams fetched
// rows, encoded as TA log lines, through an in-memory relay (source/mem) into
// the engine's NORMAL pipeline (parse → filter → identity → write) — so there
// is no custom write model and no checkpoint. These tests cover the event path
// (track upserts deduped by #uuid), the user path (user_setOnce updates), and
// idempotent re-runs.
package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/aura-studio/tango/internal/backfill"
	"github.com/aura-studio/tango/internal/role/api"
)

// mockTA serves the three TA OpenAPI endpoints over httptest with a fixed set
// of result pages.
type mockTA struct {
	headers []string
	pages   [][][]interface{}
}

func newMockTA(headers []string, pages [][][]interface{}) *mockTA {
	return &mockTA{headers: headers, pages: pages}
}

func (m *mockTA) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open/submit-sql":
			writeEnv(w, map[string]any{"taskId": "task-1"}, 0, "")
		case "/open/sql-task-info":
			var rowCount int64
			for _, p := range m.pages {
				rowCount += int64(len(p))
			}
			writeEnv(w, map[string]any{
				"taskId": "task-1", "status": backfill.StatusFinished, "progress": 100,
				"resultStat": map[string]any{
					"headers": m.headers, "pageCount": len(m.pages),
					"pageSize": 1000, "rowCount": rowCount,
				},
			}, 0, "")
		case "/open/sql-result-page":
			pid, _ := strconv.Atoi(r.URL.Query().Get("pageId"))
			// NDJSON: leading code-0 envelope line, then one JSON array per row.
			w.Header().Set("Content-Type", "application/x-ndjson")
			writeEnv(w, map[string]any{"pageId": pid}, 0, "ok")
			_, _ = w.Write([]byte("\n"))
			if pid >= 0 && pid < len(m.pages) {
				for _, row := range m.pages[pid] {
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

func writeEnv(w http.ResponseWriter, data any, code int, msg string) {
	raw, _ := json.Marshal(data)
	body, _ := json.Marshal(map[string]any{
		"return_code": code, "return_message": msg, "data": json.RawMessage(raw),
	})
	_, _ = w.Write(body)
}

func eventCfg(srvURL string) *api.BackfillConfig {
	return &api.BackfillConfig{
		APIBaseURL:    srvURL,
		Token:         "test-token",
		ProjectID:     35,
		Table:         backfill.TableEvent,
		PartDateRange: backfill.DateRange{Start: "2026-05-01", End: "2026-05-01"},
		PageSize:      1000,
		PageRetries:   1,
	}
}

// TestBackfillEventPath runs a two-page event backfill end to end and asserts
// every event landed (deduped by #uuid) via the engine upload pipeline.
func TestBackfillEventPath(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()
	ctx := context.Background()

	headers := []string{"#type", "#event_name", "#time", "#uuid", "#account_id", "level"}
	pages := [][][]interface{}{
		{{"track", "login", "2026-05-01 00:00:01", "bf-e0a", "acc", float64(1)},
			{"track", "login", "2026-05-01 00:00:02", "bf-e0b", "acc", float64(2)}},
		{{"track", "pay", "2026-05-01 00:00:03", "bf-e1a", "acc", float64(3)},
			{"track", "pay", "2026-05-01 00:00:04", "bf-e1b", "acc", float64(4)}},
	}
	srv := newMockTA(headers, pages).server(t)

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer eng.Close()
	if err := eng.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	res, err := eng.RunBackfill(ctx, eventCfg(srv.URL), nil)
	if err != nil {
		t.Fatalf("RunBackfill: %v", err)
	}
	if res.EventWrites != 4 {
		t.Errorf("event writes = %d, want 4", res.EventWrites)
	}
	if got := count(t, db, "event"); got != 4 {
		t.Errorf("event collection = %d, want 4", got)
	}

	// Re-run is idempotent: no checkpoint, but #uuid $setOnInsert dedups, so the
	// event count stays at 4.
	if _, err := eng.RunBackfill(ctx, eventCfg(srv.URL), nil); err != nil {
		t.Fatalf("re-run RunBackfill: %v", err)
	}
	if got := count(t, db, "event"); got != 4 {
		t.Errorf("after idempotent re-run, event = %d, want 4 (deduped by #uuid)", got)
	}
}

// TestBackfillUserPath runs a single-page user-table backfill and asserts the
// rows landed via the normal pipeline as user_setOnce updates. A v_user row
// carries an identity column (#account_id, so the pipeline resolves each to a
// tango #user_id — the user doc is keyed by that resolved id, consistent with
// events) and #update_time, but NO #uuid and no literal #time column. The
// encoder synthesizes a deterministic #uuid from identity and maps #update_time
// into #time (backfill.UserTimeColumn, default "#update_time"), so talog accepts
// the rows instead of dead-lettering them.
func TestBackfillUserPath(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()
	ctx := context.Background()

	headers := []string{"#account_id", "#update_time", "name", "age"}
	pages := [][][]interface{}{
		{
			{"acc-1", "2026-05-01 08:00:00.000", "Alice", float64(30)},
			{"acc-2", "2026-05-02 09:00:00.000", "Bob", float64(25)},
		},
	}
	srv := newMockTA(headers, pages).server(t)

	cfg := &api.BackfillConfig{
		APIBaseURL: srv.URL, Token: "t", ProjectID: 35,
		Table: backfill.TableUser, PageSize: 1000, PageRetries: 1,
	}

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer eng.Close()
	if err := eng.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// A small configured relay buffer (source.mem.*) exercises the mem-config
	// path and backpressure without changing the result.
	res, err := eng.RunBackfill(ctx, cfg, &api.MemConfig{BufferSize: 4})
	if err != nil {
		t.Fatalf("RunBackfill: %v", err)
	}
	if res.UserWrites != 2 {
		t.Errorf("user writes = %d, want 2 (got dead-letters=%d — does v_user carry an identity column?)", res.UserWrites, res.DeadLetters)
	}
	if got := count(t, db, "user"); got != 2 {
		t.Errorf("user collection = %d, want 2", got)
	}
}
