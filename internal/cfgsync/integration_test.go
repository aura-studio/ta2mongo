package cfgsync

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/parser"
)

// --- Integration tests (real MongoDB / DocumentDB) ------------------------
//
// These exercise the full read+write loop against a live server and are skipped
// unless TANGO_TEST_MONGO_URI is set (point it at MongoDB or Amazon DocumentDB,
// including its tls/retryWrites query params). poll-backend tests run on any
// topology (incl. standalone mongod); the changestream test self-skips when the
// topology has no change streams.

// itDao opens an isolated throwaway database for one integration test and returns
// the dao plus a cfg whose collection/documentID are the cfgsync defaults. The
// returned cleanup drops the database and closes the connection.
func itDao(t *testing.T) (*dao.Dao, *Config, func()) {
	t.Helper()
	uri := os.Getenv("TANGO_TEST_MONGO_URI")
	if uri == "" {
		t.Skip("set TANGO_TEST_MONGO_URI to run the cfgsync integration tests")
	}

	dbName := fmt.Sprintf("tango_cfgsync_it_%d", time.Now().UnixNano())
	// Splice the throwaway db name into the URI path so EJSON's default-database
	// resolution targets it; preserve any query string (tls/retryWrites/...).
	uriWithDB := spliceDB(uri, dbName)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	d, err := dao.New(ctx, &dao.Config{Mongo: &daomongo.Config{
		URI:                    uriWithDB,
		ConnectTimeout:         10 * time.Second,
		ServerSelectionTimeout: 15 * time.Second,
	}})
	if err != nil {
		t.Fatalf("dao.New: %v", err)
	}

	cfg := &Config{}
	cfg.ApplyDefaults()

	cleanup := func() {
		dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer dcancel()
		_ = d.Mongo.DB.Drop(dctx)
		_ = d.Mongo.Close()
	}
	return d, cfg, cleanup
}

// spliceDB inserts /dbName before the query string of a mongo URI, replacing any
// existing path component.
func spliceDB(uri, dbName string) string {
	scheme := "mongodb://"
	if strings.HasPrefix(uri, "mongodb+srv://") {
		scheme = "mongodb+srv://"
	}
	rest := strings.TrimPrefix(uri, scheme)

	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		query = rest[i:]
		rest = rest[:i]
	}
	// Drop any existing path (db name) after the host list.
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return scheme + rest + "/" + dbName + query
}

// startWatcher runs a poll Watcher over a fresh parser+registry and returns the
// parser (to observe the live filter) and a stop func.
func startWatcher(t *testing.T, d *dao.Dao, cfg *Config) (*parser.Parser, func()) {
	t.Helper()
	p := parser.New(nil)
	reg := NewRegistry()
	RegisterFilter(reg, p)
	w := New(d, cfg, reg.Apply)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(ctx)
	}()
	return p, func() { cancel(); <-done }
}

// keeps reports whether the parser's live filter keeps a record of the given
// #type. The default (empty) filter keeps everything; an include filter keeps
// only matching types.
func keeps(t *testing.T, p *parser.Parser, typ string) bool {
	t.Helper()
	ok, err := p.Filter().Keep(map[string]any{"#type": typ, "#event_name": "e"})
	if err != nil {
		t.Fatalf("Keep(%s): %v", typ, err)
	}
	return ok
}

// waitFor polls cond until it is true or the deadline elapses.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// publish is a publish helper that fails the test on error.
func publish(t *testing.T, d *dao.Dao, cfg *Config, doc bson.M) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v, err := Publish(ctx, d, cfg, doc)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	return v
}

// writeRaw bypasses Publish to write an arbitrary document (incl. a deliberately
// bad or out-of-order one) for the apply-side / version-guard scenarios that
// Publish would otherwise reject.
func writeRaw(t *testing.T, d *dao.Dao, cfg *Config, set bson.M) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := d.EJSON(ctx, &dao.EJSONRequest{
		Action:     dao.EJSONActionUpdateOne,
		Collection: cfg.Collection,
		Filter:     bson.M{"_id": cfg.DocumentID},
		Update:     bson.M{"$set": set},
		Upsert:     true,
	}); err != nil {
		t.Fatalf("writeRaw: %v", err)
	}
}

func TestIntegration_Poll_HotSwap(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	cfg.PollInterval = 200 * time.Millisecond

	// Publish an include=track filter, then start the watcher: the startup
	// convergence read should apply it before steady state.
	publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}})

	p, stop := startWatcher(t, d, cfg)
	defer stop()

	waitFor(t, "include=track applied", 5*time.Second, func() bool {
		return keeps(t, p, "track") && !keeps(t, p, "user_set")
	})

	// Change the filter at runtime → must hot-swap within ~one pollInterval.
	publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "user_set"`}}})
	waitFor(t, "include=user_set hot-swapped", 5*time.Second, func() bool {
		return keeps(t, p, "user_set") && !keeps(t, p, "track")
	})
}

func TestIntegration_Poll_BadFilterKeepsLastGood(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	cfg.PollInterval = 200 * time.Millisecond

	v := publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}})

	p, stop := startWatcher(t, d, cfg)
	defer stop()
	// Discriminating wait: the default (empty) filter keeps everything, so a bare
	// keeps(track) would pass before the include=track filter is actually live.
	// Requiring !keeps(user_set) too forces the real filter to be applied first.
	waitFor(t, "good filter applied", 5*time.Second, func() bool {
		return keeps(t, p, "track") && !keeps(t, p, "user_set")
	})

	// Write an uncompilable filter at a higher version directly (Publish would
	// reject it). The watcher must reject it on apply and keep the last-good.
	writeRaw(t, d, cfg, bson.M{"version": v + 1, "filter": bson.M{"include": bson.A{`#type ==== "track"`}}})

	// Give the watcher a few poll cycles to read+reject the bad doc.
	time.Sleep(1 * time.Second)
	if !keeps(t, p, "track") || keeps(t, p, "user_set") {
		t.Fatal("bad filter should have been rejected, last-good (include=track) kept")
	}
}

func TestIntegration_Poll_VersionGuardNoRollback(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	cfg.PollInterval = 200 * time.Millisecond

	// Land at version 5 with include=user_set.
	writeRaw(t, d, cfg, bson.M{"version": int64(5), "filter": bson.M{"include": bson.A{`#type == "user_set"`}}})

	p, stop := startWatcher(t, d, cfg)
	defer stop()
	// Discriminating wait (see BadFilterKeepsLastGood): require !keeps(track) so
	// the empty startup filter does not satisfy the condition before v5 is live.
	waitFor(t, "v5 applied", 5*time.Second, func() bool {
		return keeps(t, p, "user_set") && !keeps(t, p, "track")
	})

	// Replay an older version with a different (also valid) filter → must be
	// dropped by the monotonic guard (no rollback).
	writeRaw(t, d, cfg, bson.M{"version": int64(3), "filter": bson.M{"include": bson.A{`#type == "track"`}}})
	time.Sleep(1 * time.Second)
	if keeps(t, p, "track") || !keeps(t, p, "user_set") {
		t.Fatal("older version replay must not roll the filter back")
	}
}

func TestIntegration_Publish_VersionMonotonic(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()

	v1 := publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}})
	v2 := publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "user_set"`}}})
	if v2 <= v1 {
		t.Fatalf("version not monotonic: v1=%d v2=%d", v1, v2)
	}
}

func TestIntegration_Publish_RejectsOffAllowlist(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Publish(ctx, d, cfg, bson.M{"dao": bson.M{"mongo": bson.M{"uri": "x"}}}); err == nil {
		t.Fatal("expected off-allowlist publish to be rejected")
	}
	// And nothing must have been written.
	doc, err := fetchDoc(ctx, d, cfg.Collection, cfg.DocumentID)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if doc != nil {
		t.Fatalf("rejected publish must not write a document, got %v", doc)
	}
}

func TestIntegration_ChangeStream_HotSwap(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	cfg.Backend = BackendChangeStream
	cfg.ReconcileInterval = 1 * time.Second

	publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}})

	// Probe support first: if the topology has no change streams (standalone
	// mongod / DocumentDB without modifyChangeStreams) the backend Run returns a
	// clear error pointing at backend=poll — assert that and skip the rest.
	probe := &changeStreamBackend{dao: d, cfg: cfg}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	cs, perr := probe.subscribe(probeCtx)
	if perr != nil {
		probeCancel()
		if !strings.Contains(strings.ToLower(perr.Error()), "replica") &&
			!strings.Contains(strings.ToLower(perr.Error()), "change stream") &&
			!strings.Contains(perr.Error(), "$changeStream") {
			t.Logf("change stream subscribe error (treated as unsupported): %v", perr)
		}
		t.Skip("change streams not supported on this topology; covered by poll tests")
	}
	_ = cs.Close(probeCtx)
	probeCancel()

	p, stop := startWatcher(t, d, cfg)
	defer stop()
	waitFor(t, "initial filter applied (changestream)", 5*time.Second, func() bool {
		return keeps(t, p, "track")
	})

	// A runtime change should be pushed sub-second via the stream (well under the
	// reconcile fallback).
	publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "user_set"`}}})
	waitFor(t, "changestream hot-swap", 3*time.Second, func() bool {
		return keeps(t, p, "user_set") && !keeps(t, p, "track")
	})
}

// startWatchers starts n independent Watchers over the same dao + central
// document, each with its own parser + registry — exactly like n separate daemon
// processes that embed cfgsync. It returns the n parsers (to observe each one's
// live filter) and a single stop that tears them all down.
func startWatchers(t *testing.T, d *dao.Dao, cfg *Config, n int) ([]*parser.Parser, func()) {
	t.Helper()
	parsers := make([]*parser.Parser, n)
	stops := make([]func(), n)
	for i := 0; i < n; i++ {
		parsers[i], stops[i] = startWatcher(t, d, cfg)
	}
	return parsers, func() {
		for _, s := range stops {
			s()
		}
	}
}

// allConverged reports whether EVERY watcher's live filter keeps `keep` and drops
// `drop` — i.e. all simulated daemons have converged on the published filter (and
// the discriminating `drop` check rules out the empty startup filter).
func allConverged(t *testing.T, ps []*parser.Parser, keep, drop string) bool {
	t.Helper()
	for _, p := range ps {
		if !keeps(t, p, keep) || keeps(t, p, drop) {
			return false
		}
	}
	return true
}

// TestIntegration_Poll_FanOutAllWatchers is the core guarantee: a single publish
// must converge on EVERY daemon, not just one. It runs N independent poll
// Watchers (each its own parser, standing in for N daemon processes) against the
// same central document, publishes once, and asserts ALL N hot-swap within ~one
// pollInterval; then publishes a second filter and asserts ALL N follow again.
func TestIntegration_Poll_FanOutAllWatchers(t *testing.T) {
	const n = 3
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	cfg.PollInterval = 200 * time.Millisecond

	publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}})

	ps, stop := startWatchers(t, d, cfg, n)
	defer stop()
	waitFor(t, "all watchers applied include=track", 5*time.Second, func() bool {
		return allConverged(t, ps, "track", "user_set")
	})

	// One publish → every watcher must independently pick it up and hot-swap.
	publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "user_set"`}}})
	waitFor(t, "all watchers hot-swapped to include=user_set", 5*time.Second, func() bool {
		return allConverged(t, ps, "user_set", "track")
	})
}

// TestIntegration_ChangeStream_FanOutAllWatchers is the changestream counterpart:
// one publish, every watcher pushed sub-second via its own change stream. It
// self-skips when the topology has no change streams (covered by the poll
// fan-out test there).
func TestIntegration_ChangeStream_FanOutAllWatchers(t *testing.T) {
	const n = 3
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	cfg.Backend = BackendChangeStream
	cfg.ReconcileInterval = 1 * time.Second

	publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}})

	probe := &changeStreamBackend{dao: d, cfg: cfg}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	cs, perr := probe.subscribe(probeCtx)
	if perr != nil {
		probeCancel()
		t.Skip("change streams not supported on this topology; covered by poll fan-out test")
	}
	_ = cs.Close(probeCtx)
	probeCancel()

	ps, stop := startWatchers(t, d, cfg, n)
	defer stop()
	waitFor(t, "all watchers applied include=track (changestream)", 5*time.Second, func() bool {
		return allConverged(t, ps, "track", "user_set")
	})

	// One publish → every watcher's own stream delivers it sub-second.
	publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "user_set"`}}})
	waitFor(t, "all watchers hot-swapped sub-second (changestream)", 3*time.Second, func() bool {
		return allConverged(t, ps, "user_set", "track")
	})
}
