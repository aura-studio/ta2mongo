// Package tests holds repo-level integration tests. They exercise every role
// (api / cli / gateway) across every upload strategy (single / batch /
// pipeline) end-to-end against a real MongoDB, plus the daemon's continuous
// tail+report. Skipped when no MongoDB is reachable at TANGO_TEST_MONGO_URI
// (default mongodb://localhost:27017).
package tests

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/process/pipeline"
	"github.com/aura-studio/tango/internal/role/api"
	clirole "github.com/aura-studio/tango/internal/role/cli"
	"github.com/aura-studio/tango/internal/role/daemon"
	"github.com/aura-studio/tango/internal/role/gateway"
	"github.com/aura-studio/tango/internal/source"
	"github.com/aura-studio/tango/internal/source/tailer"
)

func mongoURI() string {
	if v := os.Getenv("TANGO_TEST_MONGO_URI"); v != "" {
		return v
	}
	return "mongodb://localhost:27017"
}

func pingMongo(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(mongoURI()).
		SetServerSelectionTimeout(2 * time.Second).SetConnectTimeout(2 * time.Second))
	if err != nil {
		t.Skipf("MongoDB not available: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("MongoDB not available: %v", err)
	}
	_ = client.Disconnect(ctx)
}

// freshDB returns a dao config pointed at a unique throwaway database, a verify
// handle to that database, and a cleanup func.
func freshDB(t *testing.T) (*dao.Config, *mongo.Database, func()) {
	t.Helper()
	pingMongo(t)
	dbName := fmt.Sprintf("tango_it_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	uri := mongoURI() + "/" + dbName
	daoCfg := &dao.Config{Mongo: &daomongo.Config{URI: uri}}

	vc, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("verify connect: %v", err)
	}
	db := vc.Database(dbName)
	cleanup := func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(dctx)
		_ = vc.Disconnect(dctx)
	}
	return daoCfg, db, cleanup
}

// sampleLines is a fixed mix: 4 track events, 1 user_set, 1 invalid line.
// Expected after ingestion: 4 events, 1 user, 1 dead letter.
func sampleLines() []string {
	return []string{
		`{"#type":"track","#event_name":"e1","#time":"2024-01-01","#uuid":"it-e1","#account_id":"acc"}`,
		`{"#type":"track","#event_name":"e2","#time":"2024-01-01","#uuid":"it-e2","#account_id":"acc"}`,
		`{"#type":"track","#event_name":"e3","#time":"2024-01-01","#uuid":"it-e3","#account_id":"acc"}`,
		`{"#type":"track","#event_name":"e4","#time":"2024-01-01","#uuid":"it-e4","#account_id":"acc"}`,
		`{"#type":"user_set","#time":"2024-01-01","#uuid":"it-u1","#account_id":"acc","properties":{"name":"Alice"}}`,
		"this is not json",
	}
}

func count(t *testing.T, db *mongo.Database, coll string) int64 {
	t.Helper()
	n, err := db.Collection(coll).CountDocuments(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("count %s: %v", coll, err)
	}
	return n
}

func verifyCounts(t *testing.T, db *mongo.Database) {
	t.Helper()
	if got := count(t, db, "event"); got != 4 {
		t.Errorf("events = %d, want 4", got)
	}
	if got := count(t, db, "user"); got != 1 {
		t.Errorf("users = %d, want 1", got)
	}
	if got := count(t, db, "dead_letter"); got != 1 {
		t.Errorf("dead_letters = %d, want 1", got)
	}
}

// TestRolesModes covers the 9 cases: api/cli/gateway × single/batch/pipeline.
func TestRolesModes(t *testing.T) {
	modes := []process.Mode{process.ModeSingle, process.ModeBatch, process.ModePipeline}
	roles := []string{"api", "cli", "gateway"}

	for _, role := range roles {
		for _, mode := range modes {
			role, mode := role, mode
			t.Run(role+"/"+string(mode), func(t *testing.T) {
				daoCfg, db, cleanup := freshDB(t)
				defer cleanup()
				ctx := context.Background()
				lines := sampleLines()
				procCfg := &process.Config{Mode: string(mode)}

				switch role {
				case "api":
					eng, err := api.New(ctx, daoCfg, procCfg, nil)
					if err != nil {
						t.Fatalf("api.New: %v", err)
					}
					defer eng.Close()
					if err := eng.EnsureIndexes(ctx); err != nil {
						t.Fatalf("EnsureIndexes: %v", err)
					}
					if _, err := eng.Upload(ctx, lines); err != nil {
						t.Fatalf("api Upload: %v", err)
					}
				case "cli":
					if _, err := clirole.Run(ctx, daoCfg, procCfg, nil, strings.NewReader(strings.Join(lines, "\n"))); err != nil {
						t.Fatalf("cli Run: %v", err)
					}
				case "gateway":
					srv, err := gateway.New(ctx, daoCfg, procCfg, nil, gateway.Config{})
					if err != nil {
						t.Fatalf("gateway.New: %v", err)
					}
					defer srv.Close()
					if err := srv.EnsureIndexes(ctx); err != nil {
						t.Fatalf("EnsureIndexes: %v", err)
					}
					if _, err := srv.Upload(ctx, lines); err != nil {
						t.Fatalf("gateway Upload: %v", err)
					}
				}

				verifyCounts(t, db)
			})
		}
	}
}

// TestDaemonContinuousReport is the 10th case: the daemon keeps tailing a file
// and reporting new lines appended after it has started.
func TestDaemonContinuousReport(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "ta.log")
	if err := os.WriteFile(logPath, nil, 0644); err != nil {
		t.Fatalf("create log: %v", err)
	}

	srcCfg := &source.Config{
		Tailer: &tailer.Config{
			LogPattern:     []string{filepath.Join(dir, "*.log")},
			TailMode:       tailer.TailModePoll,
			RescanInterval: 200 * time.Millisecond,
			PollInterval:   50 * time.Millisecond,
		},
	}
	srcCfg.ApplyDefaults()
	procCfg := &process.Config{Pipeline: &pipeline.Config{BatchWorkers: 1, FlushInterval: 100 * time.Millisecond}}

	svc, err := daemon.New(context.Background(), daoCfg, &parser.Config{}, srcCfg, procCfg)
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	defer svc.Shutdown()
	if err := svc.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan error, 1)
	go func() { runDone <- svc.Run(ctx) }()

	// Append events in two waves while the daemon is running.
	appendLine := func(i int) {
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			t.Errorf("open append: %v", err)
			return
		}
		fmt.Fprintf(f, `{"#type":"track","#event_name":"cont","#time":"2024-01-01","#uuid":"cont-%d","#account_id":"c"}`+"\n", i)
		_ = f.Close()
	}
	for i := 0; i < 3; i++ {
		appendLine(i)
	}
	time.Sleep(500 * time.Millisecond)
	for i := 3; i < 6; i++ {
		appendLine(i)
	}

	// Poll until all 6 events have been written (continuous ingestion).
	deadline := time.Now().Add(10 * time.Second)
	var got int64
	for time.Now().Before(deadline) {
		got = count(t, db, "event")
		if got >= 6 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	if got < 6 {
		t.Errorf("daemon ingested %d events, want >= 6 (continuous tail+report)", got)
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Error("daemon did not stop after ctx cancel")
	}
}
