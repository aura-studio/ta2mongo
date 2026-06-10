package cfgsync

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
)

// --- CS-16: runtime stream break → resubscribe (2s backoff) + full-read
// fallback, no crash; only ctx cancellation returns an error -----------------
//
// changeStreamBackend.Run's errCh branch is the recovery path under test:
//
//	case err := <-errCh:
//	    logging.WithError(err).Warn("cfgsync: change stream broke, re-subscribing")
//	    stopPump(); cs.Close; cs = b.resubscribe(ctx)   // 2s backoff between attempts
//	    b.readOnce(ctx, observe)                        // full-read fallback after the gap
//
// The break is forced by dropping the watched collection on a real replica
// set: the server emits drop+invalidate and closes the cursor. Both events
// lack documentKey, so the backend's $match{documentKey._id} pipeline filters
// them out of the batch — but the cursor still dies server-side. In driver
// v2.6.0 loopNext that surfaces as Next()==false with either Err()!=nil (the
// resume-after-invalidate aggregate is rejected) or Err()==nil && ID()==0
// (empty final batch); pump reports an error on errCh in both shapes
// (cs.Err() or context.Canceled), so Run always reaches the errCh branch.

// cs16BrokeMsg is the exact production warn line emitted by
// changeStreamBackend.Run when the pump reports a broken stream.
const cs16BrokeMsg = "cfgsync: change stream broke, re-subscribing"

// cs16LogHook counts occurrences of the exact stream-broke warn line so the
// test can prove the errCh→resubscribe branch actually ran (and did not just
// converge via some other read path).
type cs16LogHook struct {
	mu    sync.Mutex
	broke int
}

func (h *cs16LogHook) Levels() []logrus.Level { return []logrus.Level{logrus.WarnLevel} }

func (h *cs16LogHook) Fire(e *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if e.Message == cs16BrokeMsg {
		h.broke++
	}
	return nil
}

func (h *cs16LogHook) brokeCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.broke
}

// TestUltraCS16_ChangeStream_BreakResubscribe forces a runtime change-stream
// break and asserts the full CS-16 contract:
//
//  1. v1 is applied through the live stream/startup read;
//  2. dropping the watched collection breaks the stream: the backend logs the
//     exact warn line "cfgsync: change stream broke, re-subscribing" (observed
//     via a logrus hook on the shared logger) and re-subscribes — it does NOT
//     return, does NOT crash;
//  3. a v2 published after the break is applied within 15s (> resubscribeBackoff
//     2s + slack) even though ReconcileInterval is set to 10m, i.e. the ONLY
//     possible delivery paths are the post-resubscribe full read (readOnce) or
//     the re-subscribed stream itself — the periodic reconcile tick cannot mask
//     a dead recovery path;
//  4. Watcher.Run is still running after the whole episode (no error returned),
//     and returns promptly with context.Canceled once ctx is cancelled.
//
// Note on v2's version: dropping the collection deletes the config document,
// so a plain Publish would $inc-upsert a fresh document back at version 1 —
// which the watcher's monotonic guard (lastVersion) would silently drop. That
// is the documented no-rollback semantics, not a recovery failure, so the test
// re-publishes via writeRaw with an explicitly higher version (v1+100) to keep
// the recovery assertion deterministic.
func TestUltraCS16_ChangeStream_BreakResubscribe(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	cfg.Backend = BackendChangeStream
	// Push the reconcile fallback far beyond the test horizon: convergence on v2
	// within 15s can then only come from the resubscribe path (its readOnce or
	// the new stream), never from a periodic reconcile tick.
	cfg.ReconcileInterval = 10 * time.Minute

	// Probe change-stream support first (same pattern as the package's other
	// changestream integration tests): self-skip on topologies without streams.
	probe := &changeStreamBackend{dao: d, cfg: cfg}
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 10*time.Second)
	pcs, perr := probe.subscribe(probeCtx)
	if perr != nil {
		probeCancel()
		t.Skipf("change streams not supported on this topology (need a replica set): %v", perr)
	}
	_ = pcs.Close(probeCtx)
	probeCancel()

	// Hook the shared logger to observe the exact production warn line. logrus
	// has no RemoveHook; ReplaceHooks(empty) on cleanup is safe because no other
	// cfgsync test installs hooks.
	hook := &cs16LogHook{}
	logging.L().AddHook(hook)
	t.Cleanup(func() { logging.L().ReplaceHooks(make(logrus.LevelHooks)) })

	// v1: include=track. Fresh throwaway db → Publish's $inc-upsert lands at
	// exactly version 1.
	v1 := publish(t, d, cfg, bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}})
	if v1 != 1 {
		t.Fatalf("first publish into a fresh db returned version %d, want 1", v1)
	}

	// Start the watcher, capturing Run's return so the test can assert it does
	// NOT return on the stream break and DOES return context.Canceled on cancel.
	p := parser.New(nil)
	reg := NewRegistry()
	RegisterFilter(reg, p)
	w := New(d, cfg, reg.Apply)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- w.Run(ctx) }()

	// (1) v1 applied. The discriminating !keeps(user_set) rules out the default
	// empty filter (which keeps everything).
	waitFor(t, "v1 (include=track) applied via changestream backend", 10*time.Second, func() bool {
		return keeps(t, p, "track") && !keeps(t, p, "user_set")
	})

	// (2) Force the break: drop the watched collection. The server invalidates
	// the change-stream cursor; pump's cs.Next returns false and errCh fires.
	brokeBefore := hook.brokeCount()
	dropCtx, dropCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := d.Mongo.DB.Collection(cfg.Collection).Drop(dropCtx); err != nil {
		dropCancel()
		t.Fatalf("dropping watched collection %q: %v", cfg.Collection, err)
	}
	dropCancel()

	// The break must surface as the exact production warn line — this proves the
	// errCh branch ran (resubscribe path), not merely that some read converged.
	waitFor(t, `warn "`+cs16BrokeMsg+`" logged after collection drop`, 10*time.Second, func() bool {
		return hook.brokeCount() > brokeBefore
	})

	// The watcher must NOT have returned: a runtime break is recovered, not fatal.
	select {
	case err := <-runErr:
		t.Fatalf("Watcher.Run returned on stream break (must resubscribe instead): %v", err)
	default:
	}

	// (3) v2 after the break: insert (auto-recreating the collection) at an
	// explicitly higher version so the monotonic guard accepts it (see the
	// version note in the test comment). It must be applied within 15s —
	// resubscribeBackoff (2s) + generous slack — via the re-subscribed stream or
	// its post-resubscribe full read, with the 10m reconcile out of the picture.
	writeRaw(t, d, cfg, bson.M{
		"version": v1 + 100,
		"filter":  bson.M{"include": bson.A{`#type == "user_set"`}},
	})
	waitFor(t, "v2 (include=user_set) applied after resubscribe", 15*time.Second, func() bool {
		return keeps(t, p, "user_set") && !keeps(t, p, "track")
	})

	// (4) Still running after the whole break/recover episode…
	select {
	case err := <-runErr:
		t.Fatalf("Watcher.Run returned before ctx cancel: %v", err)
	default:
	}

	// …and ctx cancel returns promptly with ctx.Err() (the changestream backend
	// returns ctx.Err() from its ctx.Done arm — context.Canceled here).
	cancel()
	select {
	case err := <-runErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Watcher.Run after ctx cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watcher.Run did not return within 5s of ctx cancel")
	}

	// Exactly the recovery the contract promises: the break was logged, the
	// stream was re-subscribed, and nothing escalated into a second break.
	if got := hook.brokeCount() - brokeBefore; got < 1 {
		t.Fatalf("stream-broke warn line count = %d, want >= 1", got)
	}
}
