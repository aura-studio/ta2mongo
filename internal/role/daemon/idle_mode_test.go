package daemon

// Degraded idle mode: an empty dao.mongo.uri must not crash the daemon — the
// process starts, runs no logic (no tailer / pipeline / Mongo), and exits
// cleanly on signal. The non-idle path must be untouched: with a URI present
// the usual fail-fast validation still applies.

import (
	"context"
	"testing"
	"time"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// TestIdleMode_EmptyURIStartsAndStaysUp: Role.Run with no dao.mongo.uri keeps
// running (idle) instead of returning an error, and returns nil promptly once
// the context is cancelled (the path SIGTERM is wired to).
func TestIdleMode_EmptyURIStartsAndStaysUp(t *testing.T) {
	tree := cfgtree.New(map[string]any{
		"role": map[string]any{"mode": "daemon"},
		// dao.mongo.uri intentionally absent; logPattern absent too — idle mode
		// must not require it (nothing is tailed).
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (Role{}).Run(ctx, tree) }()

	// Must NOT return within the grace window — idle means alive, not exited.
	select {
	case err := <-done:
		t.Fatalf("daemon exited immediately in idle mode (err=%v); want it to stay up", err)
	case <-time.After(500 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("idle daemon returned %v on shutdown, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("idle daemon did not exit within 5s of ctx cancel")
	}
}

// TestIdleMode_URIPresentKeepsFailFast: with a URI configured the role must NOT
// idle — the existing validation chain runs (here: missing logPattern fails
// fast before any Mongo dial, so no database is needed for this test).
func TestIdleMode_URIPresentKeepsFailFast(t *testing.T) {
	tree := cfgtree.New(map[string]any{
		"role": map[string]any{"mode": "daemon"},
		"dao": map[string]any{"mongo": map[string]any{
			"uri": "mongodb://10.255.255.1:27017/x?serverSelectionTimeoutMS=30000",
		}},
		// logPattern intentionally missing -> NewFromTree must fail fast.
	})

	start := time.Now()
	err := (Role{}).Run(context.Background(), tree)
	if err == nil {
		t.Fatal("Run succeeded with uri set but no logPattern; want fail-fast error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("Run took %s; the URI-present path must not idle or dial Mongo before validation", elapsed)
	}
}
