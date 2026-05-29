package backfill

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
)

const testMongoURI = "mongodb://localhost:27017"

func pingMongo(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	opts := options.Client().
		ApplyURI(testMongoURI).
		SetServerSelectionTimeout(2 * time.Second).
		SetConnectTimeout(2 * time.Second)
	c, err := mongo.Connect(ctx, opts)
	if err != nil {
		t.Skipf("MongoDB not available at %s: %v", testMongoURI, err)
	}
	if err := c.Ping(ctx, nil); err != nil {
		_ = c.Disconnect(ctx)
		t.Skipf("MongoDB not available at %s: %v", testMongoURI, err)
	}
	_ = c.Disconnect(ctx)
}

// ---------------------------------------------------------------------------
// Mock TA OpenAPI server
// ---------------------------------------------------------------------------

// fakePage holds a single canned page of results.
type fakePage struct {
	Headers []string
	Rows    [][]interface{}
}

// fakeTask describes one logical SQL task as the mock TA understands it.
type fakeTask struct {
	pages       []fakePage
	rowCount    int64
	pollsBefore int     // how many task-info polls return RUNNING before FINISHED
	pollCount   int     // monotonic poll count, mutated under mu
	expireAfter int     // -1 = never; otherwise: # of result-page calls after which the task starts returning "expired"
	pageCalls   int     // mutated under mu
	mu          sync.Mutex
}

// mockTA implements the three TA OpenAPI endpoints with programmable
// per-SQL responses. Tests register canned tasks via SetResponse and the
// mock returns them in submit order.
type mockTA struct {
	mu        sync.Mutex
	responses []*fakeTask
	tasks     map[string]*fakeTask
	taskIDSeq int
	server    *httptest.Server
}

func newMockTA(t *testing.T) *mockTA {
	t.Helper()
	m := &mockTA{tasks: make(map[string]*fakeTask)}
	m.server = httptest.NewServer(http.HandlerFunc(m.handle))
	t.Cleanup(m.server.Close)
	return m
}

// SetResponse queues a fake task to be returned by the next submit-sql call.
func (m *mockTA) SetResponse(t *fakeTask) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, t)
}

// URL returns the mock server's base URL (no trailing slash).
func (m *mockTA) URL() string { return m.server.URL }

func (m *mockTA) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/open/submit-sql":
		m.submit(w, r)
	case "/open/sql-task-info":
		m.info(w, r)
	case "/open/sql-result-page":
		m.page(w, r)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, data any, code int, msg string) {
	raw, _ := json.Marshal(data)
	env := struct {
		ReturnCode    int             `json:"return_code"`
		ReturnMessage string          `json:"return_message"`
		Data          json.RawMessage `json:"data"`
	}{code, msg, raw}
	body, _ := json.Marshal(env)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func (m *mockTA) submit(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.responses) == 0 {
		writeJSON(w, nil, -1, "no canned response left")
		return
	}
	task := m.responses[0]
	m.responses = m.responses[1:]
	m.taskIDSeq++
	id := fmt.Sprintf("task-%d", m.taskIDSeq)
	m.tasks[id] = task
	writeJSON(w, map[string]string{"taskId": id}, 0, "ok")
}

func (m *mockTA) info(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("taskId")
	m.mu.Lock()
	task, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		writeJSON(w, nil, -1, "task not found")
		return
	}
	task.mu.Lock()
	task.pollCount++
	finished := task.pollCount > task.pollsBefore
	task.mu.Unlock()

	resp := TaskInfoResult{TaskID: id}
	if finished {
		resp.Status = StatusFinished
		resp.Progress = 100
		resp.ResultStat.Headers = task.pages[0].Headers
		resp.ResultStat.PageCount = len(task.pages)
		resp.ResultStat.PageSize = 1000
		resp.ResultStat.RowCount = task.rowCount
	} else {
		resp.Status = StatusRunning
		resp.Progress = 50
	}
	writeJSON(w, resp, 0, "ok")
}

func (m *mockTA) page(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("taskId")
	pageID := 0
	fmt.Sscanf(r.URL.Query().Get("pageId"), "%d", &pageID)

	m.mu.Lock()
	task, ok := m.tasks[id]
	m.mu.Unlock()
	if !ok {
		writeJSON(w, nil, -1, "task not found")
		return
	}

	task.mu.Lock()
	task.pageCalls++
	expired := task.expireAfter >= 0 && task.pageCalls > task.expireAfter
	task.mu.Unlock()

	if expired {
		// Simulate task TTL expiration: forget the task so subsequent
		// info/page calls return "task not found", which the client maps
		// to ErrTaskExpired.
		m.mu.Lock()
		delete(m.tasks, id)
		m.mu.Unlock()
		writeJSON(w, nil, -1, "task expired")
		return
	}

	if pageID < 0 || pageID >= len(task.pages) {
		writeJSON(w, nil, -1, fmt.Sprintf("page %d out of range", pageID))
		return
	}
	p := task.pages[pageID]
	// The real TA result-page endpoint returns NDJSON: first line is the
	// standard envelope wrapping page metadata, then one JSON array per row.
	meta := ResultPageResult{
		TaskID:    id,
		Headers:   p.Headers,
		PageCount: len(task.pages),
		PageSize:  1000,
		PageID:    pageID,
		RowCount:  task.rowCount,
	}
	metaRaw, _ := json.Marshal(meta)
	env := struct {
		ReturnCode    int             `json:"return_code"`
		ReturnMessage string          `json:"return_message"`
		Data          json.RawMessage `json:"data"`
	}{0, "ok", metaRaw}
	body, _ := json.Marshal(env)
	w.Header().Set("Content-Type", "application/x-ndjson")
	_, _ = w.Write(body)
	_, _ = w.Write([]byte("\n"))
	for _, row := range p.Rows {
		rowRaw, _ := json.Marshal(row)
		_, _ = w.Write(rowRaw)
		_, _ = w.Write([]byte("\n"))
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func newTestConfig(mockURL, dbName, runID string) config.Config {
	pollInterval := 5 * time.Millisecond
	pollTimeout := 10 * time.Second
	return config.Config{
		Mode:           config.ModeBackfill,
		MongoURI:       testMongoURI + "/" + dbName,
		BatchSize:      100,
		BatchWorkers:   2,
		FlushInterval:  100 * time.Millisecond,
		MaxElapsedTime: 5 * time.Second,
		LogLevel:       "warn",
		TailMode:       config.TailModeHybrid,
		Backfill: config.BackfillConfig{
			APIBaseURL:         mockURL,
			Token:              "test-token",
			ProjectID:          102,
			Table:              "event",
			PartDateRange:      config.DateRange{Start: "2026-05-01", End: "2026-05-02"},
			PageSize:           1000,
			PollInterval:       pollInterval,
			PollTimeout:        pollTimeout,
			RunID:              runID,
			ProgressCollection: config.DefaultProgressCollection,
		},
	}
}

func newTestLogger() *logrus.Logger {
	l := logrus.New()
	l.SetLevel(logrus.WarnLevel)
	return l
}

func uniqueDBName() string {
	return fmt.Sprintf("tango_backfill_test_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
}

// buildEventRow assembles a typical TA event row: system #fields + a couple
// of property columns. Helper for keeping the test data declarative.
func buildEventRow(uuid, accountID, evt string, level int, country string) []interface{} {
	return []interface{}{
		"track", evt, accountID, "did-" + uuid, "2026-05-01 10:00:00", uuid, float64(level), country,
	}
}

func eventHeaders() []string {
	return []string{"#type", "#event_name", "#account_id", "#distinct_id", "#time", "#uuid", "level", "country"}
}

func dropTestDB(t *testing.T, uri string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := mongo.Connect(ctx, options.Client().ApplyURI(uri))
	if err != nil {
		return
	}
	defer c.Disconnect(ctx)
	dbName, _ := config.MongoDBFromURI(uri)
	_ = c.Database(dbName).Drop(ctx)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRunner_HappyPath_SingleDay_SinglePage(t *testing.T) {
	pingMongo(t)

	mockTA := newMockTA(t)
	dbName := uniqueDBName()
	cfg := newTestConfig(mockTA.URL(), dbName, "happy-1")
	cfg.Backfill.PartDateRange = config.DateRange{Start: "2026-05-01", End: "2026-05-01"}
	defer dropTestDB(t, cfg.MongoURI)

	rows := [][]interface{}{
		buildEventRow("u-1", "acc-a", "login", 5, "CN"),
		buildEventRow("u-2", "acc-b", "pay", 7, "US"),
		buildEventRow("u-3", "acc-c", "login", 3, "JP"),
	}
	mockTA.SetResponse(&fakeTask{
		pages:       []fakePage{{Headers: eventHeaders(), Rows: rows}},
		rowCount:    3,
		pollsBefore: 0,
		expireAfter: -1,
	})

	ctx := context.Background()
	r, err := New(ctx, cfg, newTestLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Shutdown()
	if err := r.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Verify event docs.
	mc, _ := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	defer mc.Disconnect(ctx)
	db := mc.Database(dbName)

	count, _ := db.Collection("event").CountDocuments(ctx, bson.M{})
	if count != 3 {
		t.Errorf("event count = %d, want 3", count)
	}

	// Verify properties were flattened correctly: properties.level got promoted.
	var doc bson.M
	if err := db.Collection("event").FindOne(ctx, bson.M{"#uuid": "u-1"}).Decode(&doc); err != nil {
		t.Fatalf("find u-1: %v", err)
	}
	if got := doc["level"]; got != int32(5) && got != int64(5) && got != float64(5) {
		t.Errorf("u-1 level = %v (%T), want 5", got, got)
	}
	if got := doc["country"]; got != "CN" {
		t.Errorf("u-1 country = %v", got)
	}

	// Verify checkpoint marks day completed.
	var run Run
	if err := db.Collection(config.DefaultProgressCollection).
		FindOne(ctx, bson.M{"_id": cfg.Backfill.RunID}).Decode(&run); err != nil {
		t.Fatalf("load run: %v", err)
	}
	day := run.Days["2026-05-01"]
	if day.Status != DayCompleted {
		t.Errorf("day status = %q, want completed", day.Status)
	}
	if day.Rows != 3 {
		t.Errorf("day rows = %d, want 3", day.Rows)
	}

	// Stats sanity.
	if r.Stats().EventWrites.Load() != 3 {
		t.Errorf("EventWrites = %d, want 3", r.Stats().EventWrites.Load())
	}
}

func TestRunner_MultiDay_MultiPage(t *testing.T) {
	pingMongo(t)

	mockTA := newMockTA(t)
	dbName := uniqueDBName()
	cfg := newTestConfig(mockTA.URL(), dbName, "multi-1")
	cfg.Backfill.PartDateRange = config.DateRange{Start: "2026-05-01", End: "2026-05-02"}
	defer dropTestDB(t, cfg.MongoURI)

	// Day 1: 2 pages of 2 rows each.
	mockTA.SetResponse(&fakeTask{
		pages: []fakePage{
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("d1-a", "acc-1", "login", 1, "CN"),
				buildEventRow("d1-b", "acc-2", "login", 2, "CN"),
			}},
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("d1-c", "acc-3", "pay", 3, "US"),
				buildEventRow("d1-d", "acc-4", "pay", 4, "US"),
			}},
		},
		rowCount:    4,
		pollsBefore: 1, // one RUNNING, then FINISHED
		expireAfter: -1,
	})
	// Day 2: 1 page of 1 row.
	mockTA.SetResponse(&fakeTask{
		pages: []fakePage{
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("d2-a", "acc-5", "logout", 5, "JP"),
			}},
		},
		rowCount:    1,
		pollsBefore: 0,
		expireAfter: -1,
	})

	ctx := context.Background()
	r, err := New(ctx, cfg, newTestLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Shutdown()
	if err := r.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mc, _ := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	defer mc.Disconnect(ctx)
	count, _ := mc.Database(dbName).Collection("event").CountDocuments(ctx, bson.M{})
	if count != 5 {
		t.Errorf("event count = %d, want 5", count)
	}
	if pages := r.Stats().Pages.Load(); pages != 3 {
		t.Errorf("pages = %d, want 3 (2 day1 + 1 day2)", pages)
	}
}

func TestRunner_TaskExpiry_Resubmits_AndDedupsViaUUID(t *testing.T) {
	pingMongo(t)

	mockTA := newMockTA(t)
	dbName := uniqueDBName()
	cfg := newTestConfig(mockTA.URL(), dbName, "expire-1")
	cfg.Backfill.PartDateRange = config.DateRange{Start: "2026-05-01", End: "2026-05-01"}
	defer dropTestDB(t, cfg.MongoURI)

	// First task: returns 1 page successfully, then expires on the second page.
	mockTA.SetResponse(&fakeTask{
		pages: []fakePage{
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("x-1", "acc-1", "login", 1, "CN"),
				buildEventRow("x-2", "acc-2", "login", 2, "CN"),
			}},
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("x-3", "acc-3", "pay", 3, "US"),
			}},
		},
		rowCount:    3,
		pollsBefore: 0,
		expireAfter: 1, // page 0 ok, page 1 returns expired
	})
	// Second (resubmit) task: returns all 2 pages successfully — same data.
	mockTA.SetResponse(&fakeTask{
		pages: []fakePage{
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("x-1", "acc-1", "login", 1, "CN"),
				buildEventRow("x-2", "acc-2", "login", 2, "CN"),
			}},
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("x-3", "acc-3", "pay", 3, "US"),
			}},
		},
		rowCount:    3,
		pollsBefore: 0,
		expireAfter: -1,
	})

	ctx := context.Background()
	r, err := New(ctx, cfg, newTestLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Shutdown()
	if err := r.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mc, _ := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	defer mc.Disconnect(ctx)
	count, _ := mc.Database(dbName).Collection("event").CountDocuments(ctx, bson.M{})
	if count != 3 {
		t.Errorf("event count = %d, want 3 (dedup via #uuid)", count)
	}
}

func TestRunner_ResumeAfterRestart(t *testing.T) {
	pingMongo(t)

	mockTA := newMockTA(t)
	dbName := uniqueDBName()
	cfg := newTestConfig(mockTA.URL(), dbName, "resume-1")
	cfg.Backfill.PartDateRange = config.DateRange{Start: "2026-05-01", End: "2026-05-03"}
	defer dropTestDB(t, cfg.MongoURI)

	// Run 1: only feed responses for the first 2 days; the third day will be
	// "pending" and the runner exits after them.
	for _, day := range []string{"d1", "d2"} {
		uuid1 := day + "-a"
		uuid2 := day + "-b"
		mockTA.SetResponse(&fakeTask{
			pages: []fakePage{
				{Headers: eventHeaders(), Rows: [][]interface{}{
					buildEventRow(uuid1, "acc-x", "evt", 1, "CN"),
					buildEventRow(uuid2, "acc-y", "evt", 2, "US"),
				}},
			},
			rowCount:    2,
			pollsBefore: 0,
			expireAfter: -1,
		})
	}
	// Day 3 has no canned response; this would normally fail. Cancel the
	// run via a short-lived context before day 3 starts: simpler approach
	// is to add an "unfeed-stops-on-no-response" path. But our submit handler
	// returns return_code=-1 → SubmitSQL → APIError → runDay error →
	// MarkFailed → continue. The day is "failed", not "completed".
	mockTA.SetResponse(&fakeTask{
		pages: []fakePage{
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("d3-a", "acc-z", "evt", 3, "JP"),
			}},
		},
		rowCount:    1,
		pollsBefore: 0,
		expireAfter: -1,
	})

	ctx := context.Background()

	// First run: process all 3 days.
	{
		r, err := New(ctx, cfg, newTestLogger())
		if err != nil {
			t.Fatalf("New (1st): %v", err)
		}
		if err := r.EnsureIndexes(ctx); err != nil {
			t.Fatal(err)
		}
		if err := r.Run(ctx); err != nil {
			t.Fatalf("Run (1st): %v", err)
		}
		_ = r.Shutdown()
	}

	mc, _ := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	defer mc.Disconnect(ctx)
	count, _ := mc.Database(dbName).Collection("event").CountDocuments(ctx, bson.M{})
	if count != 5 {
		t.Errorf("after 1st run: count = %d, want 5", count)
	}

	// Second run with the SAME runID and a fresh mock with NO canned
	// responses: should be a complete no-op (all days already completed).
	mockTA2 := newMockTA(t)
	cfg.Backfill.APIBaseURL = mockTA2.URL()
	var submitCount int32
	mockTA2.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/open/submit-sql" {
			atomic.AddInt32(&submitCount, 1)
		}
		mockTA2.handle(w, r)
	})

	r2, err := New(ctx, cfg, newTestLogger())
	if err != nil {
		t.Fatalf("New (2nd): %v", err)
	}
	defer r2.Shutdown()
	if err := r2.Run(ctx); err != nil {
		t.Fatalf("Run (2nd): %v", err)
	}
	if got := atomic.LoadInt32(&submitCount); got != 0 {
		t.Errorf("2nd run made %d submit-sql calls, want 0 (all days already completed)", got)
	}
	count2, _ := mc.Database(dbName).Collection("event").CountDocuments(ctx, bson.M{})
	if count2 != 5 {
		t.Errorf("after 2nd run: count = %d, want 5 (unchanged)", count2)
	}
}

func TestRunner_FilterPushdownAndLocalAgree(t *testing.T) {
	pingMongo(t)

	mockTA := newMockTA(t)
	dbName := uniqueDBName()
	cfg := newTestConfig(mockTA.URL(), dbName, "filter-1")
	cfg.Backfill.PartDateRange = config.DateRange{Start: "2026-05-01", End: "2026-05-01"}
	// Only keep events with country == "CN".
	cfg.FilterInclude = []string{`country == "CN"`}
	defer dropTestDB(t, cfg.MongoURI)

	// In real life the SQL pushdown would filter server-side. Our mock does
	// NOT honour SQL (it returns whatever rows we feed). So we feed mixed
	// rows and rely on the LOCAL filter to drop the non-CN ones.
	mockTA.SetResponse(&fakeTask{
		pages: []fakePage{
			{Headers: eventHeaders(), Rows: [][]interface{}{
				buildEventRow("f-1", "acc-1", "login", 5, "CN"),
				buildEventRow("f-2", "acc-2", "login", 5, "US"),
				buildEventRow("f-3", "acc-3", "login", 5, "CN"),
			}},
		},
		rowCount:    3,
		pollsBefore: 0,
		expireAfter: -1,
	})

	ctx := context.Background()
	r, err := New(ctx, cfg, newTestLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Shutdown()
	if err := r.EnsureIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mc, _ := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	defer mc.Disconnect(ctx)
	count, _ := mc.Database(dbName).Collection("event").CountDocuments(ctx, bson.M{})
	if count != 2 {
		t.Errorf("after filter: count = %d, want 2 (only CN)", count)
	}
	if filtered := r.Stats().Filtered.Load(); filtered != 1 {
		t.Errorf("Filtered = %d, want 1", filtered)
	}

	// Inspect SQL sent on submit: the mock recorded it via SetResponse + log
	// not directly accessible here. Instead verify that the runner emitted a
	// SQL string containing the pushed-down predicate, by checking the buildSQL.
	got := r.buildDaySQL("2026-05-01")
	if !strings.Contains(got, `"country" = 'CN'`) {
		t.Errorf("built SQL missing pushdown predicate; got: %s", got)
	}
	if !strings.Contains(got, `"$part_date" = '2026-05-01'`) {
		t.Errorf("built SQL missing partDate; got: %s", got)
	}
}
