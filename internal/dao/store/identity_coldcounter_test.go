package store

// Regression test for the id_counter cold-start upsert race: when many workers
// concurrently resolve DISTINCT new accounts before the {_id:"user_id"} counter
// document exists, the upsert-insert collides on E11000. nextUserID must retry
// (not propagate the error), or the identity resolve fails and the event is
// dead-lettered — observed in production on DocumentDB under load.

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestIdentityResolver_ConcurrentColdCounter(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}
	ir := st.Identity()

	const k = 64 // distinct new accounts resolved concurrently from a cold counter
	errs := make([]error, k)
	ids := make([]int64, k)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < k; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start // release together to maximize the counter-create collision
			ids[i], errs[i] = ir.Resolve(ctx, fmt.Sprintf("cold-acc-%d", i), "")
		}(i)
	}
	close(start)
	wg.Wait()

	seen := make(map[int64]int, k)
	for i := 0; i < k; i++ {
		if errs[i] != nil {
			t.Errorf("Resolve(cold-acc-%d) failed: %v — nextUserID did not retry the id_counter upsert race", i, errs[i])
			continue
		}
		if ids[i] <= 0 {
			t.Errorf("Resolve(cold-acc-%d) returned non-positive user_id %d", i, ids[i])
		}
		seen[ids[i]]++
	}
	// Every distinct account must get its own unique #user_id (the counter is
	// atomic once it exists; the retry must not double-allocate or collide).
	for id, c := range seen {
		if c != 1 {
			t.Errorf("user_id %d allocated to %d accounts (counter not atomic / retry double-counts)", id, c)
		}
	}
	if len(seen) != k {
		t.Errorf("got %d distinct user_ids for %d distinct accounts, want %d", len(seen), k, k)
	}
}
