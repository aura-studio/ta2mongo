package daemon

// Pull-before-ingest gate: with cfgsync ENABLED the daemon must not ingest a
// single line until the central config document has been pulled and applied —
// "nothing published yet" means waiting (fail-closed), not ingesting on the
// baseline filter (fail-open). Reuses the Mongo helpers from watchdog_test.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/process/pipeline"
	"github.com/aura-studio/tango/internal/source"
	"github.com/aura-studio/tango/internal/source/tailer"
)

func TestPullGate_NoIngestUntilConfigApplied(t *testing.T) {
	daoCfg, db, cleanup := freshDaemonDB(t)
	defer cleanup()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "ta.log")
	// Pre-fill BEFORE the daemon starts: this is exactly the startup flood the
	// gate exists for (the tailer re-reads existing files from the top).
	var lines string
	for i := 0; i < 30; i++ {
		lines += fmt.Sprintf(`{"#type":"track","#event_name":"keepme","#time":"2026-06-10","#uuid":"pg-k-%02d","#account_id":"a"}`+"\n", i)
		lines += fmt.Sprintf(`{"#type":"track","#event_name":"dropme","#time":"2026-06-10","#uuid":"pg-d-%02d","#account_id":"a"}`+"\n", i)
	}
	if err := os.WriteFile(logPath, []byte(lines), 0644); err != nil {
		t.Fatal(err)
	}

	srcCfg := &source.Config{Tailer: &tailer.Config{
		LogPattern:     []string{filepath.Join(dir, "*.log")},
		TailMode:       tailer.TailModePoll,
		RescanInterval: 100 * time.Millisecond,
		PollInterval:   25 * time.Millisecond,
	}}
	srcCfg.ApplyDefaults()
	procCfg := &process.Config{Pipeline: &pipeline.Config{BatchWorkers: 2, FlushInterval: 80 * time.Millisecond}}
	csCfg := &cfgsync.Config{
		Enabled:           true,
		Backend:           cfgsync.BackendPoll,
		PollInterval:      80 * time.Millisecond,
		ReconcileInterval: time.Second,
	}
	csCfg.ApplyDefaults()

	svc, err := New(context.Background(), daoCfg, &parser.Config{}, srcCfg, procCfg, csCfg)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	defer svc.Shutdown()
	if err := svc.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()

	// Phase 1: no config published — the daemon must NOT ingest anything, even
	// though the log file is sitting there full of lines.
	time.Sleep(1200 * time.Millisecond)
	if got := eventCount(t, db); got != 0 {
		t.Fatalf("daemon ingested %d events BEFORE the central config was published (gate breached)", got)
	}
	select {
	case err := <-runDone:
		t.Fatalf("daemon exited while waiting for config (err=%v); want it to keep waiting", err)
	default:
	}

	// Phase 2: publish a filter keeping only event_name "keepme". Ingestion
	// must start AND start with the remote filter already live: keepme lands,
	// dropme never does.
	pubDao, err := dao.New(context.Background(), daoCfg)
	if err != nil {
		t.Fatalf("publish dao: %v", err)
	}
	defer pubDao.Mongo.Close()
	if _, err := cfgsync.Publish(context.Background(), pubDao, csCfg, bson.M{"filter": bson.M{
		"include": []string{`#event_name == "keepme"`},
	}}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	deadline := time.Now().Add(15 * time.Second)
	var got int64
	for time.Now().Before(deadline) {
		got = eventCount(t, db)
		if got >= 30 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != 30 {
		t.Fatalf("after publish event count = %d, want 30 keepme events", got)
	}
	// The remote filter must have been live from the FIRST ingested line: no
	// dropme event may exist (settle briefly to catch stragglers).
	time.Sleep(500 * time.Millisecond)
	n, err := db.Collection("event").CountDocuments(context.Background(), bson.M{"#event_name": "dropme"})
	if err != nil {
		t.Fatalf("count dropme: %v", err)
	}
	if n != 0 {
		t.Fatalf("%d 'dropme' events ingested — the filter was not applied before ingestion started", n)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop after ctx cancel")
	}
}

// TestPullGate_ShutdownWhileWaiting: cancelling the daemon while it is parked on
// the gate (no config ever published) must return promptly and cleanly.
func TestPullGate_ShutdownWhileWaiting(t *testing.T) {
	daoCfg, _, cleanup := freshDaemonDB(t)
	defer cleanup()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ta.log"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	srcCfg := &source.Config{Tailer: &tailer.Config{
		LogPattern:     []string{filepath.Join(dir, "*.log")},
		TailMode:       tailer.TailModePoll,
		RescanInterval: 100 * time.Millisecond,
		PollInterval:   25 * time.Millisecond,
	}}
	srcCfg.ApplyDefaults()
	csCfg := &cfgsync.Config{Enabled: true, Backend: cfgsync.BackendPoll, PollInterval: 80 * time.Millisecond}
	csCfg.ApplyDefaults()

	svc, err := New(context.Background(), daoCfg, &parser.Config{}, srcCfg,
		&process.Config{Pipeline: &pipeline.Config{BatchWorkers: 1, FlushInterval: 80 * time.Millisecond}}, csCfg)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	defer svc.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()

	time.Sleep(400 * time.Millisecond) // parked on the gate
	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("Run returned %v on shutdown-while-waiting, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon stuck on the gate after ctx cancel")
	}
}
