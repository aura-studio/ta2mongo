package daemon

// v1.5.1 increment tests (doc/test2.md group G): the NewFromTree wiring layer for
// the daemon role must be equivalent to the typed New path and must fail fast on
// a missing logPattern BEFORE touching Mongo. The tree is built directly with
// cfgtree.New (a leaf package) rather than config.LoadBytes, to avoid the
// config -> role -> daemon import cycle in this white-box test. (gateway/api
// NewFromTree have their own tests; the typed-New regression is G5 = the
// existing role/client/test suites.)

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// G1: a daemon built from a config tree via NewFromTree ingests end-to-end just
// like one built from typed module configs + New — same logPattern wiring,
// parser, pipeline, and Mongo writes.
func TestNewFromTree_G1_DaemonEquivalent(t *testing.T) {
	daoCfg, db, cleanup := freshDaemonDB(t)
	defer cleanup()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "ta.log")
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	tree := cfgtree.New(map[string]any{
		"role": map[string]any{"mode": "daemon"},
		"dao":  map[string]any{"mongo": map[string]any{"uri": daoCfg.Mongo.URI}},
		"source": map[string]any{"tailer": map[string]any{
			"logPattern": []string{filepath.Join(dir, "*.log")},
			"tailMode":   "poll",
		}},
		"process": map[string]any{"pipeline": map[string]any{"batchWorkers": 1}},
	})

	svc, err := NewFromTree(context.Background(), tree)
	if err != nil {
		t.Fatalf("NewFromTree: %v", err)
	}
	defer svc.Shutdown()
	if err := svc.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	appendEvents(t, logPath, 12)

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && eventCount(t, db) < 12 {
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-runDone

	if got := eventCount(t, db); got != 12 {
		t.Errorf("NewFromTree daemon ingested %d events, want 12 (wiring not equivalent to New)", got)
	}
}

// G2: with logPattern absent, NewFromTree must return the logPattern error
// *before* any Mongo connection attempt. We point dao at a non-routable address
// with a long server-selection timeout; if NewFromTree tried to connect first it
// would block for tens of seconds. A fast logPattern error proves the validation
// runs ahead of New/dao.New. No Mongo needed for this case.
func TestNewFromTree_G2_FailFastBeforeMongo(t *testing.T) {
	tree := cfgtree.New(map[string]any{
		"role": map[string]any{"mode": "daemon"},
		"dao": map[string]any{"mongo": map[string]any{
			"uri": "mongodb://10.255.255.1:27017/x?serverSelectionTimeoutMS=30000&connectTimeoutMS=30000",
		}},
		"source": map[string]any{"tailer": map[string]any{"tailMode": "poll"}},
	})

	start := time.Now()
	_, err := NewFromTree(context.Background(), tree)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("NewFromTree succeeded with no logPattern; want a fail-fast error")
	}
	if !strings.Contains(err.Error(), "logPattern") {
		t.Errorf("error = %q, want it to mention logPattern", err.Error())
	}
	if elapsed > 3*time.Second {
		t.Errorf("NewFromTree took %s — it appears to have attempted a Mongo connection before validating logPattern (not fail-fast)", elapsed)
	}
}
