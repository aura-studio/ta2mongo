// Repo-level integration tests for the v1.6.1 backfill domain: a mock
// ThinkingData OpenAPI (httptest, the submit→poll→paginate trio) feeding
// Engine.RunBackfill against a real MongoDB/DocumentDB temp database. Covers
// the event path (pages → Engine.Upload), the user path (snapshot upserts), the
// per-page checkpoint + resume after an interrupted (failed-page) run, and the
// SQL-signature drift guard.
package test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/aura-studio/tango/internal/backfill"
	"github.com/aura-studio/tango/internal/role/api"
)

// mockTA serves the three TA OpenAPI endpoints over httptest. headers/pages are
// fixed per test; failPage (when >= 0) makes that page id return HTTP 500 so the
// runner's day fails, exercising the resume path. pageHits counts result-page
// fetches per page id.
type mockTA struct {
	headers  []string
	pages    [][][]interface{}
	mu       sync.Mutex
	failPage int
	pageHits map[int]int
}

func newMockTA(headers []string, pages [][][]interface{}) *mockTA {
	return &mockTA{headers: headers, pages: pages, failPage: -1, pageHits: map[int]int{}}
}

func (m *mockTA) setFailPage(p int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failPage = p
}

func (m *mockTA) hits(page int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pageHits[page]
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
			m.mu.Lock()
			m.pageHits[pid]++
			fail := pid == m.failPage
			m.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("boom"))
				return
			}
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

func eventCfg(srvURL, runID string) *api.BackfillConfig {
	return &api.BackfillConfig{
		APIBaseURL:    srvURL,
		Token:         "test-token",
		ProjectID:     35,
		Table:         backfill.TableEvent,
		RunID:         runID,
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

	res, err := eng.RunBackfill(ctx, eventCfg(srv.URL, "it-bf-event"))
	if err != nil {
		t.Fatalf("RunBackfill: %v", err)
	}
	if res.EventWrites != 4 {
		t.Errorf("event writes = %d, want 4", res.EventWrites)
	}
	if got := count(t, db, "event"); got != 4 {
		t.Errorf("event collection = %d, want 4", got)
	}
}

// TestBackfillUserPath runs a single-page user-table backfill and asserts the
// snapshot upserts landed keyed by #user_id.
func TestBackfillUserPath(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()
	ctx := context.Background()

	headers := []string{"#user_id", "name", "age"}
	pages := [][][]interface{}{
		{{"u1", "Alice", float64(30)}, {"u2", "Bob", float64(25)}},
	}
	srv := newMockTA(headers, pages).server(t)

	cfg := &api.BackfillConfig{
		APIBaseURL: srv.URL, Token: "t", ProjectID: 35,
		Table: backfill.TableUser, RunID: "it-bf-user", PageSize: 1000, PageRetries: 1,
	}

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer eng.Close()
	if err := eng.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	res, err := eng.RunBackfill(ctx, cfg)
	if err != nil {
		t.Fatalf("RunBackfill: %v", err)
	}
	if res.UserWrites != 2 {
		t.Errorf("user writes = %d, want 2", res.UserWrites)
	}
	if got := count(t, db, "user"); got != 2 {
		t.Errorf("user collection = %d, want 2", got)
	}
	var alice bson.M
	if err := db.Collection("user").FindOne(ctx, bson.M{"#user_id": "u1"}).Decode(&alice); err != nil {
		t.Fatalf("find u1: %v", err)
	}
	if alice["name"] != "Alice" {
		t.Errorf("u1 name = %v, want Alice", alice["name"])
	}
}

// TestBackfillResume interrupts a run by failing page 1, asserts the per-page
// checkpoint persisted page 0, then re-runs (same RunID) and asserts the run
// completes with every event present (idempotent re-import).
func TestBackfillResume(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()
	ctx := context.Background()

	headers := []string{"#type", "#event_name", "#time", "#uuid", "#account_id"}
	pages := [][][]interface{}{
		{{"track", "login", "2026-05-01 00:00:01", "rs-0a", "acc"},
			{"track", "login", "2026-05-01 00:00:02", "rs-0b", "acc"}},
		{{"track", "pay", "2026-05-01 00:00:03", "rs-1a", "acc"},
			{"track", "pay", "2026-05-01 00:00:04", "rs-1b", "acc"}},
	}
	m := newMockTA(headers, pages)
	srv := m.server(t)

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer eng.Close()
	if err := eng.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// Run 1: page 1 fails → the day fails after page 0 is checkpointed.
	m.setFailPage(1)
	if _, err := eng.RunBackfill(ctx, eventCfg(srv.URL, "it-bf-resume")); err != nil {
		t.Fatalf("run 1 RunBackfill: %v", err)
	}
	if got := count(t, db, "event"); got != 2 {
		t.Fatalf("after interrupted run, event = %d, want 2 (page 0 only)", got)
	}
	// The checkpoint persisted page 0 of the day and did not mark it completed.
	day := progressDay(t, db, "it-bf-resume", "2026-05-01")
	if day.Status == backfill.DayCompleted {
		t.Errorf("day should not be completed after page-1 failure: %v", day.Status)
	}
	if day.PageID != 0 {
		t.Errorf("checkpoint pageId = %d, want 0 (per-page flush)", day.PageID)
	}

	// Run 2: pages all succeed; same RunID resumes the pending day to completion.
	m.setFailPage(-1)
	if _, err := eng.RunBackfill(ctx, eventCfg(srv.URL, "it-bf-resume")); err != nil {
		t.Fatalf("run 2 RunBackfill: %v", err)
	}
	if got := count(t, db, "event"); got != 4 {
		t.Errorf("after resume, event = %d, want 4 (all pages, deduped)", got)
	}
	day = progressDay(t, db, "it-bf-resume", "2026-05-01")
	if day.Status != backfill.DayCompleted {
		t.Errorf("day status after resume = %v, want completed", day.Status)
	}
}

// TestBackfillSignatureMismatch refuses to resume a RunID whose defining filter
// changed (the SQLSignature guard).
func TestBackfillSignatureMismatch(t *testing.T) {
	daoCfg, _, cleanup := freshDB(t)
	defer cleanup()
	ctx := context.Background()

	headers := []string{"#type", "#event_name", "#time", "#uuid", "#account_id"}
	pages := [][][]interface{}{{{"track", "login", "2026-05-01 00:00:01", "sg-0", "acc"}}}
	srv := newMockTA(headers, pages).server(t)

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer eng.Close()
	if err := eng.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	if _, err := eng.RunBackfill(ctx, eventCfg(srv.URL, "it-bf-sig")); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	// Same RunID, different selection (Events changes the SQL signature).
	cfg2 := eventCfg(srv.URL, "it-bf-sig")
	cfg2.Events = []string{"login"}
	_, err = eng.RunBackfill(ctx, cfg2)
	if err == nil {
		t.Fatal("expected SQL signature mismatch error on config drift")
	}
}

// progressDay returns the per-day checkpoint state from the _backfill_progress
// run, decoded into the typed backfill.Run.
func progressDay(t *testing.T, db *mongo.Database, runID, day string) backfill.DayProgress {
	t.Helper()
	var run backfill.Run
	if err := db.Collection(backfill.DefaultProgressCollection).
		FindOne(context.Background(), bson.M{"_id": runID}).Decode(&run); err != nil {
		t.Fatalf("load progress %s: %v", runID, err)
	}
	p, ok := run.Days[day]
	if !ok {
		t.Fatalf("progress.days[%s] missing: %+v", day, run.Days)
	}
	return p
}
