// Integration test for the poison-document fix: a permanently-unwritable document
// (here, one that exceeds the server's command/BSON size limit) must NOT wedge the
// pipeline in an infinite bulk-write retry. It is quarantined to dead_letter while
// the rest of the batch is written. Pre-fix this batch wedged the pipeline until
// the context deadline; post-fix it completes promptly.
//
// Gated on TANGO_TEST_MONGO_URI via freshDB (skips without a real MongoDB/DocumentDB).
package test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/role/api"
)

func eventLine(uuid, acc string, extra map[string]any) string {
	doc := map[string]any{
		"#type": "track", "#event_name": "e", "#time": "2026-06-26 00:00:00",
		"#uuid": uuid, "#account_id": acc,
	}
	if extra != nil {
		doc["properties"] = extra
	}
	b, _ := json.Marshal(doc)
	return string(b)
}

func TestPipelinePoisonQuarantinedNoWedge(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()

	procCfg := &process.Config{Mode: string(process.ModePipeline)}
	procCfg.ApplyDefaults()
	parserCfg := &parser.Config{}
	parserCfg.ApplyDefaults()

	eng, err := api.New(context.Background(), daoCfg, procCfg, parserCfg, nil)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer eng.Close()
	if err := eng.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	good1 := eventLine("u-good-1", "acc-1", nil)
	good2 := eventLine("u-good-2", "acc-2", nil)
	// A ~17 MB property value makes this event's document exceed the server's
	// command/BSON size limit: the poison. It will never be writable, so it must be
	// quarantined, not retried forever.
	poison := eventLine("u-poison-1", "acc-3", map[string]any{"blob": strings.Repeat("x", 17*1024*1024)})

	// A bounded context proves "no wedge": pre-fix Upload would block here until the
	// deadline; post-fix it returns well within it.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	start := time.Now()
	res, err := eng.Upload(ctx, []string{good1, poison, good2})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Upload returned error (the poison should be quarantined, not surfaced): %v", err)
	}
	if ctx.Err() != nil {
		t.Fatalf("Upload hit the %s deadline -> still wedging on the poison (ctx err=%v)", elapsed, ctx.Err())
	}
	t.Logf("Upload returned in %s: %+v", elapsed, res)

	// Both good events landed despite the poison in the same submission.
	if got := count(t, db, "event"); got != 2 {
		t.Errorf("event count=%d, want 2 (good docs written despite the poison)", got)
	}
	// The poison was quarantined to dead_letter (not lost, not retried forever).
	q, err := db.Collection("dead_letter").CountDocuments(context.Background(),
		bson.M{"_quarantine_reason": bson.M{"$exists": true}})
	if err != nil {
		t.Fatalf("count dead_letter quarantine docs: %v", err)
	}
	if q < 1 {
		t.Errorf("expected >=1 quarantine doc in dead_letter, got %d", q)
	}
	t.Logf("PASS: 2 good events written, %d poison quarantined to dead_letter, no wedge (%s)", q, elapsed)
}
