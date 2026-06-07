package cfgsync

import (
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// newTestWatcher builds a Watcher with no dao (observe never touches it) and a
// defaulted config, recording every document onChange receives.
func newTestWatcher(onChange func(bson.M) error) *Watcher {
	cfg := &Config{}
	cfg.ApplyDefaults()
	return New(nil, cfg, onChange)
}

func TestObserve_VersionGuardMonotonic(t *testing.T) {
	var applied []int64
	w := newTestWatcher(func(doc bson.M) error {
		v, _ := docVersion(doc)
		applied = append(applied, v)
		return nil
	})

	// Increasing versions are all applied.
	for _, v := range []int64{1, 2, 5} {
		if err := w.observe(bson.M{"version": v, "filter": bson.M{}}); err != nil {
			t.Fatalf("observe v%d: %v", v, err)
		}
	}
	// Equal and older versions (replays / out-of-order) are dropped — no rollback.
	for _, v := range []int64{5, 4, 1} {
		if err := w.observe(bson.M{"version": v, "filter": bson.M{}}); err != nil {
			t.Fatalf("observe replay v%d: %v", v, err)
		}
	}
	// A newer version after the replays is applied again.
	if err := w.observe(bson.M{"version": int64(6), "filter": bson.M{}}); err != nil {
		t.Fatalf("observe v6: %v", err)
	}

	want := []int64{1, 2, 5, 6}
	if len(applied) != len(want) {
		t.Fatalf("applied versions = %v, want %v", applied, want)
	}
	for i := range want {
		if applied[i] != want[i] {
			t.Fatalf("applied versions = %v, want %v", applied, want)
		}
	}
}

func TestObserve_NilDocAndMissingVersionAreNoOps(t *testing.T) {
	calls := 0
	w := newTestWatcher(func(bson.M) error { calls++; return nil })

	if err := w.observe(nil); err != nil {
		t.Fatalf("observe(nil): %v", err)
	}
	if err := w.observe(bson.M{"filter": bson.M{}}); err != nil { // no version
		t.Fatalf("observe(no version): %v", err)
	}
	if calls != 0 {
		t.Fatalf("onChange called %d times, want 0", calls)
	}
}

func TestObserve_ApplyErrorAdvancesGuardAndKeepsLastGood(t *testing.T) {
	calls := 0
	w := newTestWatcher(func(doc bson.M) error {
		calls++
		return errors.New("bad config")
	})

	// A bad config at v1: error is surfaced (counted) but does not crash.
	if err := w.observe(bson.M{"version": int64(1), "filter": bson.M{}}); err == nil {
		t.Fatal("expected onChange error to propagate")
	}
	// The guard advanced, so the same bad version is not retried (no spin).
	if err := w.observe(bson.M{"version": int64(1), "filter": bson.M{}}); err != nil {
		t.Fatalf("replayed bad version should be dropped silently: %v", err)
	}
	if calls != 1 {
		t.Fatalf("onChange called %d times, want 1 (bad version not retried)", calls)
	}
}

func TestObserve_NilOnChangeIsReadOnly(t *testing.T) {
	w := newTestWatcher(nil)
	if err := w.observe(bson.M{"version": int64(1), "filter": bson.M{}}); err != nil {
		t.Fatalf("observe with nil onChange: %v", err)
	}
	if w.lastVersion != 1 {
		t.Fatalf("lastVersion = %d, want 1 (guard still advances)", w.lastVersion)
	}
}

func TestDocVersion(t *testing.T) {
	cases := []struct {
		name string
		doc  bson.M
		want int64
		ok   bool
	}{
		{"int64", bson.M{"version": int64(7)}, 7, true},
		{"int32", bson.M{"version": int32(7)}, 7, true},
		{"int", bson.M{"version": 7}, 7, true},
		{"float64", bson.M{"version": float64(7)}, 7, true},
		{"missing", bson.M{}, 0, false},
		{"string", bson.M{"version": "7"}, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := docVersion(c.doc)
			if got != c.want || ok != c.ok {
				t.Fatalf("docVersion = (%d, %v), want (%d, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestSelectBackend(t *testing.T) {
	if b, err := selectBackend(nil, &Config{Backend: BackendPoll}); err != nil {
		t.Fatalf("poll: %v", err)
	} else if _, ok := b.(*pollBackend); !ok {
		t.Fatalf("poll backend type = %T", b)
	}
	if b, err := selectBackend(nil, &Config{Backend: BackendChangeStream}); err != nil {
		t.Fatalf("changestream: %v", err)
	} else if _, ok := b.(*changeStreamBackend); !ok {
		t.Fatalf("changestream backend type = %T", b)
	}
	if _, err := selectBackend(nil, &Config{Backend: "bogus"}); err == nil {
		t.Fatal("expected error for unknown backend")
	}
}
