package cfgsync

// Watcher.Ready: the signal the daemon's pull-before-ingest gate blocks on.
// Integration — needs TANGO_TEST_MONGO_URI (itDao skips otherwise).

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestWatcherReady_GatesOnFirstApply: Ready() stays open while nothing is
// published (even after poll cycles run) and closes only after the first
// document is applied — the contract the daemon's pull-before-ingest gate
// relies on.
func TestWatcherReady_GatesOnFirstApply(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()

	wcfg := *cfg
	wcfg.Enabled = true
	wcfg.Backend = BackendPoll
	wcfg.PollInterval = 50 * time.Millisecond
	wcfg.ReconcileInterval = time.Second

	w := New(d, &wcfg, nil) // nil onChange: ready closes on first accepted doc
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Several poll cycles with no document: must NOT become ready.
	select {
	case <-w.Ready():
		t.Fatal("Ready() fired with no document published — gate would open on nothing")
	case <-time.After(400 * time.Millisecond):
	}

	if _, err := Publish(context.Background(), d, &wcfg, bson.M{"filter": bson.M{
		"include": []string{`#type == "a"`},
	}}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-w.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("Ready() did not fire within 5s of the document being published")
	}
}
