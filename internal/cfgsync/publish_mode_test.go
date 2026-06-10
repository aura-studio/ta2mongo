package cfgsync

// Publish-mode (set vs append) + Fetch + Watcher.Ready tests. Integration —
// needs TANGO_TEST_MONGO_URI (itDao skips otherwise); each test runs in a
// throwaway database via the shared itDao helper.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// includeOf extracts filter.include from a fetched doc as []string.
func includeOf(t *testing.T, doc bson.M) []string {
	t.Helper()
	sub, err := asSubDocument("filter", doc["filter"])
	if err != nil {
		t.Fatalf("filter subtree: %v", err)
	}
	rules, err := toStringSlice(sub["include"])
	if err != nil {
		t.Fatalf("filter.include: %v", err)
	}
	return rules
}

// excludeOf extracts filter.exclude from a fetched doc as []string.
func excludeOf(t *testing.T, doc bson.M) []string {
	t.Helper()
	sub, err := asSubDocument("filter", doc["filter"])
	if err != nil {
		t.Fatalf("filter subtree: %v", err)
	}
	rules, err := toStringSlice(sub["exclude"])
	if err != nil {
		t.Fatalf("filter.exclude: %v", err)
	}
	return rules
}

// TestPublishAppend_MergesAndPreservesOmittedSide is the set-vs-append
// differentiator: append unions include and KEEPS the stored exclude when the
// delta omits it; a subsequent set replaces the whole filter and drops it.
func TestPublishAppend_MergesAndPreservesOmittedSide(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	ctx := context.Background()

	v, err := Publish(ctx, d, cfg, bson.M{"filter": bson.M{
		"include": []string{`#type == "a"`},
		"exclude": []string{`#type == "x"`},
	}})
	if err != nil || v != 1 {
		t.Fatalf("seed set: v=%d err=%v, want v=1", v, err)
	}

	// Append only an include rule — exclude is omitted on purpose.
	v, err = PublishAppend(ctx, d, cfg, bson.M{"filter": bson.M{
		"include": []string{`#type == "b"`},
	}})
	if err != nil || v != 2 {
		t.Fatalf("append: v=%d err=%v, want v=2", v, err)
	}

	doc, err := Fetch(ctx, d, cfg)
	if err != nil || doc == nil {
		t.Fatalf("fetch after append: doc=%v err=%v", doc, err)
	}
	inc, exc := includeOf(t, doc), excludeOf(t, doc)
	if len(inc) != 2 || inc[0] != `#type == "a"` || inc[1] != `#type == "b"` {
		t.Errorf("append include = %v, want stored-then-new [a b]", inc)
	}
	if len(exc) != 1 || exc[0] != `#type == "x"` {
		t.Errorf("append must PRESERVE omitted exclude; got %v", exc)
	}

	// Now a plain set with only include: the whole filter is replaced and the
	// stored exclude is dropped — the historical replace semantics.
	if _, err := Publish(ctx, d, cfg, bson.M{"filter": bson.M{
		"include": []string{`#type == "c"`},
	}}); err != nil {
		t.Fatalf("set after append: %v", err)
	}
	doc, _ = Fetch(ctx, d, cfg)
	if inc := includeOf(t, doc); len(inc) != 1 || inc[0] != `#type == "c"` {
		t.Errorf("set include = %v, want [c] (full replace)", inc)
	}
	if exc := excludeOf(t, doc); len(exc) != 0 {
		t.Errorf("set must DROP the omitted exclude; got %v", exc)
	}
}

// TestPublishAppend_NoDocCreatesV1: append on an empty collection degenerates
// to a first publish at version 1.
func TestPublishAppend_NoDocCreatesV1(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()

	v, err := PublishAppend(context.Background(), d, cfg, bson.M{"filter": bson.M{
		"include": []string{`#type == "a"`},
	}})
	if err != nil || v != 1 {
		t.Fatalf("append-on-empty: v=%d err=%v, want v=1", v, err)
	}
	doc, _ := Fetch(context.Background(), d, cfg)
	if inc := includeOf(t, doc); len(inc) != 1 || inc[0] != `#type == "a"` {
		t.Errorf("include = %v, want [a]", inc)
	}
}

// TestPublishAppend_DedupesExactRules: re-appending an existing rule does not
// duplicate it (exact-string identity), though the version still advances.
func TestPublishAppend_DedupesExactRules(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	ctx := context.Background()

	mustAppend := func(rule string) int64 {
		t.Helper()
		v, err := PublishAppend(ctx, d, cfg, bson.M{"filter": bson.M{"include": []string{rule}}})
		if err != nil {
			t.Fatalf("append %q: %v", rule, err)
		}
		return v
	}
	mustAppend(`#type == "a"`)
	mustAppend(`#type == "b"`)
	v := mustAppend(`#type == "a"`) // duplicate

	doc, _ := Fetch(ctx, d, cfg)
	if inc := includeOf(t, doc); len(inc) != 2 {
		t.Errorf("include = %v, want exactly [a b] (no duplicate)", inc)
	}
	if v != 3 {
		t.Errorf("version = %d, want 3 (every append advances)", v)
	}
}

// TestPublishAppend_ConcurrentAppendsAllLand: the optimistic version guard must
// make concurrent appends merge, not overwrite — every distinct rule survives
// and the version counts every landed write exactly once.
func TestPublishAppend_ConcurrentAppendsAllLand(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := Publish(ctx, d, cfg, bson.M{"filter": bson.M{
		"include": []string{`#type == "seed"`},
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = PublishAppend(ctx, d, cfg, bson.M{"filter": bson.M{
				"include": []string{fmt.Sprintf(`#type == "t%d"`, i)},
			}})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent append %d failed: %v", i, err)
		}
	}

	doc, err := Fetch(ctx, d, cfg)
	if err != nil || doc == nil {
		t.Fatalf("fetch: doc=%v err=%v", doc, err)
	}
	inc := includeOf(t, doc)
	if len(inc) != n+1 {
		t.Fatalf("include has %d rules (%v), want %d — a concurrent append was lost", len(inc), inc, n+1)
	}
	if ver, _ := docVersion(doc); ver != n+1 {
		t.Errorf("version = %d, want %d (seed 1 + %d appends)", ver, n+1, n)
	}
}

// TestPublishWithMode_Dispatch: "" and "set" replace, "append" merges, anything
// else is rejected before any write.
func TestPublishWithMode_Dispatch(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := PublishWithMode(ctx, d, cfg, bson.M{"filter": bson.M{"include": []string{`#type == "a"`}}}, ""); err != nil {
		t.Fatalf("mode \"\": %v", err)
	}
	if _, err := PublishWithMode(ctx, d, cfg, bson.M{"filter": bson.M{"include": []string{`#type == "b"`}}}, PublishModeAppend); err != nil {
		t.Fatalf("mode append: %v", err)
	}
	doc, _ := Fetch(ctx, d, cfg)
	if inc := includeOf(t, doc); len(inc) != 2 {
		t.Errorf("after set+append include = %v, want 2 rules", inc)
	}
	if _, err := PublishWithMode(ctx, d, cfg, bson.M{"filter": bson.M{"include": []string{`#type == "c"`}}}, "merge"); err == nil {
		t.Error("mode \"merge\" accepted; want rejection")
	}
}

// TestFetch_NilWhenAbsent: the query face reports "nothing published" as
// (nil, nil), and returns the full document (with version) once published.
func TestFetch_NilWhenAbsent(t *testing.T) {
	d, cfg, cleanup := itDao(t)
	defer cleanup()
	ctx := context.Background()

	doc, err := Fetch(ctx, d, cfg)
	if err != nil || doc != nil {
		t.Fatalf("fetch on empty = (%v, %v), want (nil, nil)", doc, err)
	}
	if _, err := Publish(ctx, d, cfg, bson.M{"filter": bson.M{"include": []string{`#type == "a"`}}}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	doc, err = Fetch(ctx, d, cfg)
	if err != nil || doc == nil {
		t.Fatalf("fetch after publish = (%v, %v), want doc", doc, err)
	}
	if ver, ok := docVersion(doc); !ok || ver != 1 {
		t.Errorf("fetched version = %d ok=%v, want 1", ver, ok)
	}
}

