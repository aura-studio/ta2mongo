package daemon

// v1.5.1 increment tests (doc/test2.md groups D, E, F): the fd watchdog
// (source.tailer.maxOpenFDs), the runtime-stats log line, and graceful drain on
// watchdog trip — including under backpressure.
//
// Functional cases drive a real daemon Service against a throwaway Mongo
// database (TANGO_TEST_MONGO_URI, default mongodb://localhost:27017; skipped when
// unreachable). They shrink statsReportInterval so the 60s watchdog tick does not
// stall the suite — the production default (60s) is exercised by E1's predicate
// unit test and by code inspection.

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/process/pipeline"
	"github.com/aura-studio/tango/internal/source"
	"github.com/aura-studio/tango/internal/source/tailer"
)

// ---------------------------------------------------------------------------
// E1 / E3 / E4 — watchdog predicate (no Mongo, runs everywhere)
// ---------------------------------------------------------------------------

func TestWatchdog_E1E3E4_Predicate(t *testing.T) {
	cases := []struct {
		name      string
		openFDs   int
		threshold int
		want      bool
	}{
		{"E1 default off (threshold 0)", 10_000, 0, false},
		{"E1 disabled (negative threshold)", 10_000, -1, false},
		{"E3 boundary: equal does not trip", 50, 50, false},
		{"E3 boundary: one over trips", 51, 50, true},
		{"E3 far over trips", 999, 50, true},
		{"E4 non-Linux unknown (-1) inert", -1, 50, false},
		{"E4 non-Linux unknown (-1) inert vs tiny threshold", -1, 1, false},
	}
	for _, c := range cases {
		if got := fdWatchdogTriggered(c.openFDs, c.threshold); got != c.want {
			t.Errorf("%s: fdWatchdogTriggered(%d,%d)=%v want %v", c.name, c.openFDs, c.threshold, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// D2 — openFDCount (Linux: positive and ~matches /proc/self/fd)
// ---------------------------------------------------------------------------

func TestProcStats_D2_OpenFDCount(t *testing.T) {
	got := openFDCount()
	if runtime.GOOS != "linux" {
		if got != -1 {
			t.Fatalf("off Linux openFDCount()=%d, want -1 (unknown)", got)
		}
		return
	}
	if got < 0 {
		t.Fatalf("on Linux openFDCount()=%d, want >= 0", got)
	}
	// Compare to a direct /proc/self/fd listing; allow ±2 slack for fds opened or
	// closed between the two reads (and the -1 ReadDir self-fd correction).
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	direct := len(entries)
	if diff := got - (direct - 1); diff < -2 || diff > 2 {
		t.Fatalf("openFDCount()=%d not within ±2 of /proc/self/fd count %d (-1 self) = %d", got, direct, direct-1)
	}
}

// ---------------------------------------------------------------------------
// Mongo test harness
// ---------------------------------------------------------------------------

func mongoURI() string {
	if v := os.Getenv("TANGO_TEST_MONGO_URI"); v != "" {
		return v
	}
	return "mongodb://localhost:27017"
}

// withDBName injects the default database into a Mongo URI before any query
// string (so DocumentDB's tls/retryWrites params survive).
func withDBName(uri, db string) string {
	base, query := uri, ""
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		base, query = uri[:i], uri[i:]
	}
	return strings.TrimRight(base, "/") + "/" + db + query
}

func freshDaemonDB(t *testing.T) (*dao.Config, *mongo.Database, func()) {
	t.Helper()
	uri := mongoURI()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri).
		SetServerSelectionTimeout(3 * time.Second).SetConnectTimeout(3 * time.Second))
	if err != nil {
		t.Skipf("MongoDB not available: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("MongoDB not available: %v", err)
	}
	dbName := fmt.Sprintf("tango_daemon_test_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	daoCfg := &dao.Config{Mongo: &daomongo.Config{URI: withDBName(uri, dbName)}}
	db := client.Database(dbName)
	cleanup := func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dcancel()
		_ = db.Drop(dctx)
		_ = client.Disconnect(dctx)
	}
	return daoCfg, db, cleanup
}

// buildDaemon constructs a Service tailing a fresh temp file with the given
// tailer config, and returns the service, the log file path, and its dir.
func buildDaemon(t *testing.T, daoCfg *dao.Config, tcfg *tailer.Config) (*Service, string, string) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ta.log")
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if len(tcfg.LogPattern) == 0 {
		tcfg.LogPattern = []string{filepath.Join(dir, "*.log")}
	}
	srcCfg := &source.Config{Tailer: tcfg}
	procCfg := &process.Config{Pipeline: &pipeline.Config{BatchWorkers: 2, FlushInterval: 80 * time.Millisecond}}
	svc, err := New(context.Background(), daoCfg, &parser.Config{}, srcCfg, procCfg, nil)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	if err := svc.EnsureIndexes(context.Background()); err != nil {
		_ = svc.Shutdown()
		t.Fatalf("EnsureIndexes: %v", err)
	}
	return svc, logPath, dir
}

func trackEvent(i int) string {
	return fmt.Sprintf(`{"#type":"track","#event_name":"wd","#time":"2026-06-09","#uuid":"wd-%05d","#account_id":"a"}`, i)
}

func appendEvents(t *testing.T, path string, n int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	defer f.Close()
	for i := 0; i < n; i++ {
		if _, err := f.WriteString(trackEvent(i) + "\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func eventCount(t *testing.T, db *mongo.Database) int64 {
	t.Helper()
	n, err := db.Collection("event").CountDocuments(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("count event: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// log capture
// ---------------------------------------------------------------------------

type logCapture struct {
	mu      sync.Mutex
	entries []logrus.Entry
}

func (c *logCapture) Levels() []logrus.Level { return logrus.AllLevels }
func (c *logCapture) Fire(e *logrus.Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := logrus.Entry{Level: e.Level, Message: e.Message, Data: logrus.Fields{}}
	for k, v := range e.Data {
		cp.Data[k] = v
	}
	c.entries = append(c.entries, cp)
	return nil
}
func (c *logCapture) find(msgSubstr string) (logrus.Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if strings.Contains(e.Message, msgSubstr) {
			return e, true
		}
	}
	return logrus.Entry{}, false
}

func installCapture(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	l := logging.L()
	old := logrus.LevelHooks{}
	for lvl, hs := range l.Hooks {
		old[lvl] = hs
	}
	l.AddHook(c)
	t.Cleanup(func() { l.ReplaceHooks(old) })
	return c
}

// ---------------------------------------------------------------------------
// E2 — watchdog graceful restart drains in-flight, no data loss (CORE GATE)
// ---------------------------------------------------------------------------

func TestWatchdog_E2_GracefulRestartDrainsNoLoss(t *testing.T) {
	daoCfg, db, cleanup := freshDaemonDB(t)
	defer cleanup()

	restore := statsReportInterval
	statsReportInterval = 400 * time.Millisecond
	defer func() { statsReportInterval = restore }()

	cap := installCapture(t)

	// Low threshold: the process always holds far more than 3 fds (Mongo pool,
	// runtime, the tailed file), so the watchdog trips on the first 400ms tick.
	svc, logPath, _ := buildDaemon(t, daoCfg, &tailer.Config{
		TailMode:       tailer.TailModePoll,
		RescanInterval: 100 * time.Millisecond,
		PollInterval:   25 * time.Millisecond,
		MaxOpenFDs:     3,
	})
	defer svc.Shutdown()

	const n = 40
	appendEvents(t, logPath, n) // present before Run, read within ~one poll

	// Background ctx that we never cancel: the only thing that can stop Run is the
	// watchdog's internal cancelRun.
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(context.Background()) }()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("daemon Run returned error on watchdog restart: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("watchdog did not trigger a graceful restart within 20s")
	}

	// (1) the watchdog logged its restart, (2) drain completed: every event read
	// before the trip is in Mongo (no loss).
	if _, ok := cap.find("triggering graceful restart"); !ok {
		t.Errorf("expected an ERROR log 'triggering graceful restart' from the watchdog")
	}
	if got := eventCount(t, db); got != n {
		t.Errorf("after watchdog drain event count = %d, want %d (in-flight data lost)", got, n)
	}
}

// ---------------------------------------------------------------------------
// E5 — normal SIGTERM-equivalent shutdown is not disturbed by the watchdog
// ---------------------------------------------------------------------------

func TestWatchdog_E5_NormalShutdownUnaffected(t *testing.T) {
	daoCfg, db, cleanup := freshDaemonDB(t)
	defer cleanup()

	restore := statsReportInterval
	statsReportInterval = 300 * time.Millisecond
	defer func() { statsReportInterval = restore }()

	cap := installCapture(t)

	// Watchdog configured but with a huge threshold so it never trips; shutdown
	// must come solely from the ctx cancel (the path SIGTERM is wired to).
	svc, logPath, _ := buildDaemon(t, daoCfg, &tailer.Config{
		TailMode:       tailer.TailModePoll,
		RescanInterval: 100 * time.Millisecond,
		PollInterval:   25 * time.Millisecond,
		MaxOpenFDs:     1_000_000,
	})
	defer svc.Shutdown()

	const n = 30
	appendEvents(t, logPath, n)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()

	// Let it ingest and emit at least one stats tick, then SIGTERM-equivalent.
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && eventCount(t, db) < n {
		time.Sleep(100 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned error on normal shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not shut down within 10s of ctx cancel")
	}

	if got := eventCount(t, db); got != n {
		t.Errorf("normal shutdown event count = %d, want %d", got, n)
	}
	// The watchdog must NOT have fired, and logFinalStats must have run.
	if _, ok := cap.find("triggering graceful restart"); ok {
		t.Errorf("watchdog fired on a normal shutdown (threshold should never be hit)")
	}
	if _, ok := cap.find("shutdown summary"); !ok {
		t.Errorf("expected logFinalStats 'shutdown summary' on clean shutdown")
	}
}

// ---------------------------------------------------------------------------
// D1 / D3 — runtime stats line carries goroutines / open_fds / tailed_files
// ---------------------------------------------------------------------------

func TestRuntimeStats_D1_D3_Fields(t *testing.T) {
	daoCfg, db, cleanup := freshDaemonDB(t)
	defer cleanup()

	restore := statsReportInterval
	statsReportInterval = 300 * time.Millisecond
	defer func() { statsReportInterval = restore }()

	cap := installCapture(t)

	svc, logPath, _ := buildDaemon(t, daoCfg, &tailer.Config{
		TailMode:       tailer.TailModePoll,
		RescanInterval: 100 * time.Millisecond,
		PollInterval:   25 * time.Millisecond,
	})
	defer svc.Shutdown()

	appendEvents(t, logPath, 5)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()

	// Wait for at least one runtime-stats tick (≥ statsReportInterval).
	deadline := time.Now().Add(6 * time.Second)
	var e logrus.Entry
	var ok bool
	for time.Now().Before(deadline) {
		if e, ok = cap.find("runtime stats"); ok {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	cancel()
	<-runDone

	if !ok {
		t.Fatal("no 'report: runtime stats' line logged within the window (D1)")
	}
	for _, field := range []string{"goroutines", "open_fds", "tailed_files"} {
		if _, has := e.Data[field]; !has {
			t.Errorf("runtime stats line missing field %q (have %v)", field, e.Data)
		}
	}
	// D3: tailed_files reflects the one active glob file.
	if tf, _ := e.Data["tailed_files"].(int); tf != 1 {
		t.Errorf("tailed_files = %v, want 1 (one active log file)", e.Data["tailed_files"])
	}
	// On Linux open_fds is a real positive count.
	if runtime.GOOS == "linux" {
		if of, _ := e.Data["open_fds"].(int); of <= 0 {
			t.Errorf("open_fds = %v, want > 0 on Linux", e.Data["open_fds"])
		}
	}
	_ = db
}

// ---------------------------------------------------------------------------
// F2 — watchdog trips under backpressure; drain still completes (no deadlock)
// ---------------------------------------------------------------------------

func TestWatchdog_F2_DrainsUnderBackpressure(t *testing.T) {
	daoCfg, db, cleanup := freshDaemonDB(t)
	defer cleanup()

	restore := statsReportInterval
	statsReportInterval = 400 * time.Millisecond
	defer func() { statsReportInterval = restore }()

	// One worker + tiny flush window keeps batches in flight (mild backpressure)
	// while the watchdog trips, exercising drain-under-load rather than an idle
	// drain. Low threshold trips on the first tick.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "ta.log")
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatal(err)
	}
	srcCfg := &source.Config{Tailer: &tailer.Config{
		LogPattern:     []string{filepath.Join(dir, "*.log")},
		TailMode:       tailer.TailModePoll,
		RescanInterval: 100 * time.Millisecond,
		PollInterval:   20 * time.Millisecond,
		MaxOpenFDs:     3,
	}}
	procCfg := &process.Config{Pipeline: &pipeline.Config{BatchWorkers: 1, FlushInterval: 50 * time.Millisecond}}
	svc, err := New(context.Background(), daoCfg, &parser.Config{}, srcCfg, procCfg, nil)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	defer svc.Shutdown()
	if err := svc.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	const n = 200
	appendEvents(t, logPath, n)

	start := time.Now()
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(context.Background()) }()

	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run errored under backpressure watchdog: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("watchdog drain DEADLOCKED under backpressure (Run did not return in 30s)")
	}
	t.Logf("F2: watchdog drain under backpressure completed in %s", time.Since(start).Round(time.Millisecond))

	// Drain must not have lost the events it had already read.
	if got := eventCount(t, db); got != n {
		t.Errorf("after backpressure watchdog drain event count = %d, want %d", got, n)
	}
}
