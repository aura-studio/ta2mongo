// Real-ThinkingData end-to-end backfill tests: drive Engine.RunBackfill against
// the LIVE TA OpenAPI (the async submit-sql → poll → result-page trio, over a
// socks5 proxy) into a real DocumentDB temp database, for BOTH the event and the
// user table. Unlike TestBackfill{Event,User}Path (which feed a httptest MOCK
// whose headers were hand-authored to include #time/#uuid), this hits the real
// project's views — surfacing whatever the live schema actually carries.
//
// Gated on env so the normal gate skips it (like the mock tests gate on
// TANGO_TEST_MONGO_URI):
//
//	TA_BASE     e.g. https://api.lnk.events
//	TA_TOKEN    querySql/openapi token
//	TA_PROXY    e.g. socks5://user:pass@host:1080  (optional; direct if empty)
//	TA_PROJECT  numeric project id (e.g. 35 -> v_event_35 / v_user_35)
//	TA_EVENT_DAY  optional YYYY-MM-DD partition for the event table (default below)
//
// plus TANGO_TEST_MONGO_URI for the DocumentDB sink (shared with freshDB).
package test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/aura-studio/tango/internal/backfill"
	"github.com/aura-studio/tango/internal/role/api"
)

// realTAEnv reads the live-TA connection from env; the test skips when unset.
func realTAEnv(t *testing.T) (base, token, proxy string, project int) {
	t.Helper()
	base, token, proxy = os.Getenv("TA_BASE"), os.Getenv("TA_TOKEN"), os.Getenv("TA_PROXY")
	p := os.Getenv("TA_PROJECT")
	if base == "" || token == "" || p == "" {
		t.Skip("real-TA env not set (need TA_BASE / TA_TOKEN / TA_PROJECT)")
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatalf("TA_PROJECT=%q not an int: %v", p, err)
	}
	return base, token, proxy, n
}

// TestRealTABackfillEvent backfills a capped slice of the live event table
// (v_event_<project>, one partition day, LIMIT 50) and reports what landed.
func TestRealTABackfillEvent(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()
	base, token, proxy, project := realTAEnv(t)
	day := os.Getenv("TA_EVENT_DAY")
	if day == "" {
		day = "2026-06-25"
	}
	ctx := context.Background()

	cfg := &api.BackfillConfig{
		APIBaseURL:    base,
		Token:         token,
		Proxy:         proxy,
		ProjectID:     project,
		Table:         backfill.TableEvent,
		PartDateRange: backfill.DateRange{Start: day, End: day},
		Limit:         50,
		PageSize:      1000,
		PageRetries:   2,
	}

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer eng.Close()
	if err := eng.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	res, err := eng.RunBackfill(ctx, cfg, nil) // nil = default mem buffer
	if err != nil {
		t.Fatalf("RunBackfill(event): %v", err)
	}
	t.Logf("[event] result: lines=%d eventWrites=%d userWrites=%d deadLetters=%d filtered=%d",
		res.Lines, res.EventWrites, res.UserWrites, res.DeadLetters, res.Filtered)
	t.Logf("[event] collections: event=%d dead_letter=%d", count(t, db, "event"), count(t, db, "dead_letter"))

	// Post-fix expectation: headers come from the querySql probe (TA omits them
	// from the wide-SELECT* event task-info), and #event_time is mapped into #time
	// — so real event rows ingest instead of being silently dropped.
	if res.EventWrites == 0 {
		t.Errorf("[event] 0 event writes from live v_event_%d %s (lines=%d, deadLetters=%d) — "+
			"expected the headers-probe + #event_time→#time mapping to ingest rows",
			project, day, res.Lines, res.DeadLetters)
		return
	}
	t.Logf("[event] PASS: %d/%d rows written (%d dead-lettered), #event_time mapped to #time",
		res.EventWrites, res.Lines, res.DeadLetters)
	// NOTE on idempotency: tango's event SQL is `SELECT * FROM v_event_<id>
	// WHERE "$part_date"=... LIMIT n` with NO ORDER BY, so a re-run samples a
	// DIFFERENT set of rows — a count-based idempotency assertion is invalid for a
	// LIMIT sample (it grows by the non-overlap). #uuid $setOnInsert idempotency is
	// covered deterministically by TestBackfillEventPath (mock, fixed rows). Here we
	// only re-run to confirm a second live run still succeeds without error.
	if _, err := eng.RunBackfill(ctx, cfg, nil); err != nil {
		t.Fatalf("RunBackfill(event re-run): %v", err)
	}
	t.Logf("[event] re-run OK (count now %d; growth expected — LIMIT sample is non-deterministic)", count(t, db, "event"))
}

// TestRealTABackfillUser backfills a capped slice of the live user table
// (v_user_<project>, LIMIT 50) and reports what landed. This is the path the
// v1.7.1 fix targets: real v_user rows frequently carry null #uuid and have no
// #time column, so the encoder must synthesize #uuid (from identity) and map a
// time column into #time, else every row dead-letters.
func TestRealTABackfillUser(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()
	base, token, proxy, project := realTAEnv(t)
	ctx := context.Background()

	cfg := &api.BackfillConfig{
		APIBaseURL:  base,
		Token:       token,
		Proxy:       proxy,
		ProjectID:   project,
		Table:       backfill.TableUser,
		Limit:       50,
		PageSize:    1000,
		PageRetries: 2,
		// UserTimeColumn defaults to "#update_time" (ApplyDefaults).
	}

	eng, err := api.New(ctx, daoCfg, nil, nil, nil)
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	defer eng.Close()
	if err := eng.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// A small configured relay buffer also exercises source.mem.* end to end.
	res, err := eng.RunBackfill(ctx, cfg, &api.MemConfig{BufferSize: 256})
	if err != nil {
		t.Fatalf("RunBackfill(user): %v", err)
	}
	t.Logf("[user] result: lines=%d eventWrites=%d userWrites=%d deadLetters=%d filtered=%d",
		res.Lines, res.EventWrites, res.UserWrites, res.DeadLetters, res.Filtered)
	t.Logf("[user] collections: user=%d dead_letter=%d", count(t, db, "user"), count(t, db, "dead_letter"))

	// The v1.7.1 fix under test: live v_user rows frequently carry null #uuid and
	// have no #time column, so every row would dead-letter without synthesis.
	if res.UserWrites == 0 {
		t.Errorf("[user] 0 user writes from live v_user_%d (deadLetters=%d) — "+
			"the #uuid/#time synthesis (v1.7.1) is the thing under test", project, res.DeadLetters)
		return
	}
	// Some live rows carry neither #account_id nor #distinct_id and legitimately
	// dead-letter (identity cannot be resolved) — that is correct, not a defect of
	// the synthesis. So dead letters are informational, not a failure.
	t.Logf("[user] PASS: %d/%d rows written, %d dead-lettered (no-identity rows; synthesized #uuid + mapped/fallback #time made the rest parse)",
		res.UserWrites, res.Lines, res.DeadLetters)

	// NOTE on idempotency: tango's user SQL is `SELECT * FROM v_user_<id> LIMIT n`
	// with NO ORDER BY, so a re-run samples a DIFFERENT set of rows — a count-based
	// idempotency assertion is invalid for a LIMIT sample (it grows by the
	// non-overlap). user_setOnce idempotency (keyed by the resolved #user_id) is
	// covered deterministically by TestBackfillUserPath (mock, fixed rows). Here we
	// only re-run to confirm a second live run still succeeds without error.
	if _, err := eng.RunBackfill(ctx, cfg, &api.MemConfig{BufferSize: 256}); err != nil {
		t.Fatalf("RunBackfill(user re-run): %v", err)
	}
	t.Logf("[user] re-run OK (count now %d; growth expected — LIMIT sample is non-deterministic)", count(t, db, "user"))
}
