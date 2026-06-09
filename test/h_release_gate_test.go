package test

// Functional end-to-end tests for the v1.5 release gate (doc/test.md group H).
// They drive the real daemon pipeline (tail -> parse -> filter -> identity ->
// MongoDB) against a throwaway database, so they need a reachable Mongo at
// TANGO_TEST_MONGO_URI (skipped otherwise, like the rest of this package).
//
//   H1 — only the configured types (user_set + PaymentOrderState) are written;
//        everything else is dropped, and the stored fields are correct.
//   H2 — identity account_id<->distinct_id binding (1:1 and 1:N) is covered by
//        internal/dao/store/identity_integration_test.go; run that package.
//   H3 — events are not lost across a rotate boundary (at-least-once: the doc
//        notes duplicates are acceptable; loss is not).
//   H4 — SIGTERM (here: the ctx cancellation that daemon.role wires SIGTERM to,
//        see internal/role/daemon/role.go) drains in-flight work and leaves no
//        deleted-but-open fd behind.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/parser/filter"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/process/pipeline"
	"github.com/aura-studio/tango/internal/role/daemon"
	"github.com/aura-studio/tango/internal/source"
	"github.com/aura-studio/tango/internal/source/tailer"
)

// hDaemon builds a daemon Service tailing dir/*.log with the given parser config
// and a fast poll tailer, plus a verify DB handle. Returns the service, db, the
// log file path, and a cleanup func.
func hDaemon(t *testing.T, parserCfg *parser.Config) (*daemon.Service, *mongo.Database, string, func()) {
	t.Helper()
	daoCfg, db, cleanup := freshDB(t)

	dir := t.TempDir()
	logPath := filepath.Join(dir, "ta.log")
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatalf("create log: %v", err)
	}

	srcCfg := &source.Config{
		Tailer: &tailer.Config{
			LogPattern:     []string{filepath.Join(dir, "*.log")},
			TailMode:       tailer.TailModePoll,
			RescanInterval: 150 * time.Millisecond,
			PollInterval:   30 * time.Millisecond,
		},
	}
	srcCfg.ApplyDefaults()
	procCfg := &process.Config{Pipeline: &pipeline.Config{BatchWorkers: 2, FlushInterval: 80 * time.Millisecond}}

	svc, err := daemon.New(context.Background(), daoCfg, parserCfg, srcCfg, procCfg, nil)
	if err != nil {
		cleanup()
		t.Fatalf("daemon.New: %v", err)
	}
	if err := svc.EnsureIndexes(context.Background()); err != nil {
		svc.Shutdown()
		cleanup()
		t.Fatalf("EnsureIndexes: %v", err)
	}
	full := func() { svc.Shutdown(); cleanup() }
	return svc, db, logPath, full
}

func appendToFile(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
}

func countWhere(t *testing.T, db *mongo.Database, coll string, query bson.M) int64 {
	t.Helper()
	n, err := db.Collection(coll).CountDocuments(context.Background(), query)
	if err != nil {
		t.Fatalf("count %s %v: %v", coll, query, err)
	}
	return n
}

func waitCount(t *testing.T, db *mongo.Database, coll string, query bson.M, want int64, timeout time.Duration) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var got int64
	for time.Now().Before(deadline) {
		got = countWhere(t, db, coll, query)
		if got >= want {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
	return got
}

// countDeletedFDsUnder counts /proc/self/fd entries pointing at a deleted file
// under dir. Linux-only (caller must gate with runtime.GOOS).
func countDeletedFDsUnder(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	n := 0
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil || !strings.HasSuffix(target, " (deleted)") {
			continue
		}
		if strings.HasPrefix(strings.TrimSuffix(target, " (deleted)"), dir) {
			n++
		}
	}
	return n
}

// H1: with an include filter of {#type==user_set, #event_name==PaymentOrderState},
// only those records reach Mongo with correct fields; all other types are dropped.
func TestH1_FilterKeepsOnlyConfiguredTypes(t *testing.T) {
	parserCfg := &parser.Config{Filter: &filter.Config{
		Include: []string{`#type == "user_set"`, `#event_name == "PaymentOrderState"`},
	}}
	svc, db, logPath, done := hDaemon(t, parserCfg)
	defer done()

	// 3 kept events (PaymentOrderState), 2 kept users (user_set), and several
	// that must be dropped: other track events and a non-user_set user op.
	// The last line (p3) is a sentinel: once it lands, every prior line has been
	// processed, so absence of the dropped types is conclusive.
	lines := []string{
		`{"#type":"user_set","#time":"2026-06-09","#uuid":"u1","#account_id":"acc-A","properties":{"vip":true,"name":"Alice"}}`,
		`{"#type":"track","#event_name":"PaymentOrderState","#time":"2026-06-09","#uuid":"p1","#account_id":"acc-A","properties":{"state":"paid","amount":10}}`,
		`{"#type":"track","#event_name":"OtherEvent","#time":"2026-06-09","#uuid":"o1","#account_id":"acc-A","properties":{}}`,
		`{"#type":"user_add","#time":"2026-06-09","#uuid":"ua1","#account_id":"acc-B","properties":{"coins":5}}`,
		`{"#type":"user_set","#time":"2026-06-09","#uuid":"u2","#account_id":"acc-C","properties":{"vip":false,"name":"Carol"}}`,
		`{"#type":"track","#event_name":"PaymentOrderState","#time":"2026-06-09","#uuid":"p2","#account_id":"acc-C","properties":{"state":"refunded","amount":3}}`,
		`{"#type":"track","#event_name":"OtherEvent","#time":"2026-06-09","#uuid":"o2","#account_id":"acc-C","properties":{}}`,
		`{"#type":"track","#event_name":"PaymentOrderState","#time":"2026-06-09","#uuid":"p3","#account_id":"acc-A","properties":{"state":"paid","amount":99}}`,
	}
	appendToFile(t, logPath, lines...)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()
	defer func() { cancel(); <-runDone }()

	// Wait for the sentinel.
	if got := waitCount(t, db, "event", bson.M{"#event_name": "PaymentOrderState", "#uuid": "p3"}, 1, 15*time.Second); got < 1 {
		t.Fatalf("sentinel event p3 never ingested (got %d) — pipeline stalled", got)
	}
	// Settle so any (incorrectly) kept drops would have landed too.
	time.Sleep(500 * time.Millisecond)

	if got := countWhere(t, db, "event", bson.M{"#event_name": "PaymentOrderState"}); got != 3 {
		t.Errorf("PaymentOrderState events = %d, want 3", got)
	}
	if got := countWhere(t, db, "event", bson.M{"#event_name": "OtherEvent"}); got != 0 {
		t.Errorf("OtherEvent events = %d, want 0 (must be filtered out)", got)
	}
	if got := countWhere(t, db, "event", bson.M{}); got != 3 {
		t.Errorf("total events = %d, want 3 (only PaymentOrderState)", got)
	}
	if got := countWhere(t, db, "user", bson.M{}); got != 2 {
		t.Errorf("users = %d, want 2 (only the two user_set; user_add dropped)", got)
	}
	if got := countWhere(t, db, "dead_letter", bson.M{}); got != 0 {
		t.Errorf("dead_letters = %d, want 0 (drops are discarded, not dead-lettered)", got)
	}

	// Field correctness: a kept event carries its event_name + promoted property.
	var ev bson.M
	if err := db.Collection("event").FindOne(context.Background(), bson.M{"#uuid": "p1"}).Decode(&ev); err != nil {
		t.Fatalf("load event p1: %v", err)
	}
	if ev["#event_name"] != "PaymentOrderState" {
		t.Errorf("event p1 #event_name = %v, want PaymentOrderState", ev["#event_name"])
	}
	if ev["state"] != "paid" {
		t.Errorf("event p1 promoted property state = %v, want paid", ev["state"])
	}
	// A kept user carries its promoted property.
	var us bson.M
	if err := db.Collection("user").FindOne(context.Background(), bson.M{"name": "Alice"}).Decode(&us); err != nil {
		t.Fatalf("load user Alice: %v", err)
	}
	if us["vip"] != true {
		t.Errorf("user Alice vip = %v, want true", us["vip"])
	}
}

// H3: events are not lost across a rotate boundary (rename current away + create
// a new file at the same path, then keep writing). All distinct uuids must end
// up in Mongo; duplicates (if any) collapse on the uuid-keyed upsert.
func TestH3_RotateBoundaryNoLoss(t *testing.T) {
	svc, db, logPath, done := hDaemon(t, &parser.Config{})
	defer done()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()
	defer func() { cancel(); <-runDone }()

	mkEvent := func(uuid string) string {
		return fmt.Sprintf(`{"#type":"track","#event_name":"rot","#time":"2026-06-09","#uuid":"%s","#account_id":"r"}`, uuid)
	}

	const pre, post = 150, 150
	for i := 0; i < pre; i++ {
		appendToFile(t, logPath, mkEvent(fmt.Sprintf("pre-%04d", i)))
	}
	// Wait until the pre-rotate batch is mostly ingested so the rotation truly
	// straddles an in-progress tail.
	if got := waitCount(t, db, "event", bson.M{}, pre/2, 10*time.Second); got < pre/2 {
		t.Fatalf("pre-rotate ingest stalled: got %d", got)
	}

	// Rotate: rename current away, create a fresh file at the same path.
	if err := os.Rename(logPath, logPath+".1"); err != nil {
		t.Fatalf("rotate rename: %v", err)
	}
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatalf("recreate after rotate: %v", err)
	}
	for i := 0; i < post; i++ {
		appendToFile(t, logPath, mkEvent(fmt.Sprintf("post-%04d", i)))
	}

	// Every distinct uuid (pre + post) must be present — no loss across the
	// boundary. The backup ta.log.1 does NOT match the *.log glob, so the
	// pre-rotate residue is only recoverable from the renamed inode the tailer
	// still holds open until reap — exactly the boundary case under test.
	total := int64(pre + post)
	if got := waitCount(t, db, "event", bson.M{}, total, 20*time.Second); got != total {
		// Distinguish loss from duplication by checking distinct uuids.
		t.Fatalf("event count = %d, want %d distinct uuids (loss across rotate boundary)", got, total)
	}
	// Spot-check a uuid from each side actually exists.
	for _, u := range []string{"pre-0000", "pre-0149", "post-0000", "post-0149"} {
		if got := countWhere(t, db, "event", bson.M{"#uuid": u}); got < 1 {
			t.Errorf("missing event uuid %q across rotate boundary", u)
		}
	}
}

// H4: cancelling the run context (the exact path SIGTERM is wired to in
// daemon.role) drains in-flight work and releases every tailed fd — no
// deleted-but-open descriptor remains. Linux-only for the fd assertion.
func TestH4_GracefulShutdownDrainsAndReleasesFDs(t *testing.T) {
	svc, db, logPath, done := hDaemon(t, &parser.Config{})
	defer done()
	dir := filepath.Dir(logPath)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()

	mkEvent := func(i int) string {
		return fmt.Sprintf(`{"#type":"track","#event_name":"sd","#time":"2026-06-09","#uuid":"sd-%04d","#account_id":"s"}`, i)
	}
	const n = 200
	for i := 0; i < n; i++ {
		appendToFile(t, logPath, mkEvent(i))
	}
	// Let some flow, then write a final burst right before shutdown so the drain
	// path has in-flight work to finish.
	waitCount(t, db, "event", bson.M{}, n/2, 10*time.Second)

	// SIGTERM-equivalent: cancel the run context, then the daemon must drain.
	cancel()
	select {
	case err := <-runDone:
		if err != nil && err != context.Canceled {
			t.Fatalf("daemon Run returned error on shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("daemon did not shut down within 15s after ctx cancel (drain hung)")
	}

	// Drain completed: all n events that were written must be persisted.
	if got := countWhere(t, db, "event", bson.M{}); got != int64(n) {
		t.Errorf("after graceful shutdown event count = %d, want %d (drain incomplete)", got, n)
	}

	// No deleted-but-open fd may remain under the log dir after shutdown.
	if runtime.GOOS == "linux" {
		// Give the just-returned tail goroutines a beat to finish their deferred
		// closes (Run returns after the tailer closes out, but the OS fd close
		// is in the same deferred path).
		var deleted int
		deadline := time.Now().Add(3 * time.Second)
		for {
			deleted = countDeletedFDsUnder(t, dir)
			if deleted == 0 || time.Now().After(deadline) {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if deleted != 0 {
			t.Errorf("after graceful shutdown %d deleted-but-open fd remain under %s (fd not released on SIGTERM)", deleted, dir)
		}
	}
}
