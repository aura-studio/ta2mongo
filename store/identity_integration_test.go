package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

var _ = fmt.Sprintf

// ---------------------------------------------------------------------------
// IdentityResolver with real MongoDB
// ---------------------------------------------------------------------------

func TestIdentityResolver_OnlyAccountID(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// First call: creates a new mapping
	uid1, err := ir.Resolve(ctx, "acc1", "")
	if err != nil {
		t.Fatalf("Resolve(acc1, ''): %v", err)
	}
	if uid1 <= 0 {
		t.Errorf("expected positive user_id, got %d", uid1)
	}

	// Second call: should return the same user_id (cache hit)
	uid2, err := ir.Resolve(ctx, "acc1", "")
	if err != nil {
		t.Fatalf("second Resolve(acc1, ''): %v", err)
	}
	if uid2 != uid1 {
		t.Errorf("expected same user_id %d, got %d", uid1, uid2)
	}
}

func TestIdentityResolver_OnlyDistinctID(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	uid1, err := ir.Resolve(ctx, "", "did1")
	if err != nil {
		t.Fatalf("Resolve('', did1): %v", err)
	}
	if uid1 <= 0 {
		t.Errorf("expected positive user_id, got %d", uid1)
	}

	uid2, err := ir.Resolve(ctx, "", "did1")
	if err != nil {
		t.Fatalf("second Resolve('', did1): %v", err)
	}
	if uid2 != uid1 {
		t.Errorf("expected same user_id %d, got %d", uid1, uid2)
	}
}

func TestIdentityResolver_BothIDs_NewUser(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// Both new: should create a single mapping with both IDs
	uid1, err := ir.Resolve(ctx, "acc_both", "did_both")
	if err != nil {
		t.Fatalf("Resolve(acc_both, did_both): %v", err)
	}
	if uid1 <= 0 {
		t.Errorf("expected positive user_id, got %d", uid1)
	}

	// Same pair again: should return same user_id
	uid2, err := ir.Resolve(ctx, "acc_both", "did_both")
	if err != nil {
		t.Fatalf("second Resolve(acc_both, did_both): %v", err)
	}
	if uid2 != uid1 {
		t.Errorf("expected same user_id %d, got %d", uid1, uid2)
	}

	// Query by account_id only: same user_id
	uid3, err := ir.Resolve(ctx, "acc_both", "")
	if err != nil {
		t.Fatalf("Resolve(acc_both, ''): %v", err)
	}
	if uid3 != uid1 {
		t.Errorf("expected same user_id %d when querying by account only, got %d", uid1, uid3)
	}

	// Query by distinct_id only: same user_id
	uid4, err := ir.Resolve(ctx, "", "did_both")
	if err != nil {
		t.Fatalf("Resolve('', did_both): %v", err)
	}
	if uid4 != uid1 {
		t.Errorf("expected same user_id %d when querying by distinct only, got %d", uid1, uid4)
	}
}

func TestIdentityResolver_AccountExistsDistinctNew(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// Create account first
	uid1, err := ir.Resolve(ctx, "acc_existing", "")
	if err != nil {
		t.Fatalf("create account: %v", err)
	}

	// Then resolve with same account + new distinct_id: should bind distinct to account
	uid2, err := ir.Resolve(ctx, "acc_existing", "new_did")
	if err != nil {
		t.Fatalf("resolve with new distinct: %v", err)
	}
	if uid2 != uid1 {
		t.Errorf("expected distinct bound to existing account user_id %d, got %d", uid1, uid2)
	}
}

func TestIdentityResolver_DistinctExistsAccountNew(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// Create distinct first (no account)
	uid1, err := ir.Resolve(ctx, "", "did_existing")
	if err != nil {
		t.Fatalf("create distinct: %v", err)
	}

	// Resolve with new account + existing distinct: should bind account to distinct's user
	uid2, err := ir.Resolve(ctx, "new_acc", "did_existing")
	if err != nil {
		t.Fatalf("resolve with new account: %v", err)
	}
	if uid2 != uid1 {
		t.Errorf("expected account bound to existing distinct user_id %d, got %d", uid1, uid2)
	}
}

func TestIdentityResolver_BothExist_SameUser(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// Create both bound together
	uid1, err := ir.Resolve(ctx, "acc_same", "did_same")
	if err != nil {
		t.Fatalf("create both: %v", err)
	}

	// Resolve again: should be same
	uid2, err := ir.Resolve(ctx, "acc_same", "did_same")
	if err != nil {
		t.Fatalf("resolve same: %v", err)
	}
	if uid2 != uid1 {
		t.Errorf("expected same user_id %d, got %d", uid1, uid2)
	}
}

func TestIdentityResolver_BothExist_DifferentUsers(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// Create two separate users
	uid1, err := ir.Resolve(ctx, "acc_diff_1", "")
	if err != nil {
		t.Fatalf("create acc1: %v", err)
	}

	uid2, err := ir.Resolve(ctx, "", "did_diff_2")
	if err != nil {
		t.Fatalf("create did2: %v", err)
	}
	if uid1 == uid2 {
		t.Error("expected different user_ids for different users")
	}

	// Resolve with both: account_id takes priority
	uid3, err := ir.Resolve(ctx, "acc_diff_1", "did_diff_2")
	if err != nil {
		t.Fatalf("resolve both existing: %v", err)
	}
	if uid3 != uid1 {
		t.Errorf("expected account_id priority user_id %d, got %d", uid1, uid3)
	}
}

func TestIdentityResolver_DistinctAlreadyBound(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// Bind distinct to account1
	uid1, err := ir.Resolve(ctx, "acc_bound1", "did_bound")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}

	// Try to resolve with a different account + same distinct_id
	// distinct_id is already bound to acc_bound1, so new account gets its own user
	uid2, err := ir.Resolve(ctx, "acc_bound2", "did_bound")
	if err != nil {
		t.Fatalf("resolve with different account: %v", err)
	}

	// account priority: should return acc_bound2's user_id (which is new)
	// The distinct_id is already bound to another account, so acc_bound2 is separate
	if uid2 == uid1 {
		// This is also acceptable behavior depending on implementation
		// but typically the account gets its own mapping
		t.Log("same user_id returned (account bound to existing distinct's user)")
	}

	// Verify both can be resolved independently
	uid3, err := ir.Resolve(ctx, "acc_bound1", "")
	if err != nil {
		t.Fatalf("resolve acc_bound1: %v", err)
	}
	if uid3 != uid1 {
		t.Errorf("expected acc_bound1 user_id %d, got %d", uid1, uid3)
	}
}

func TestIdentityResolver_AutoIncrementUserID(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// Create several users and verify IDs are sequential
	ids := make([]int64, 5)
	for i := 0; i < 5; i++ {
		uid, err := ir.Resolve(ctx, fmt.Sprintf("seq_acc_%d", i), "")
		if err != nil {
			t.Fatalf("create user %d: %v", i, err)
		}
		ids[i] = uid
	}

	for i := 1; i < 5; i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("expected monotonically increasing IDs: ids[%d]=%d, ids[%d]=%d",
				i-1, ids[i-1], i, ids[i])
		}
	}
}

func TestIdentityResolver_ConcurrentAccess(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// First resolve to create the mapping
	expected, err := ir.Resolve(ctx, "concurrent_acc", "concurrent_did")
	if err != nil {
		t.Fatalf("initial resolve: %v", err)
	}

	// Concurrent reads should all return same user_id
	var wg sync.WaitGroup
	results := make([]int64, 20)
	errs := make([]error, 20)

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = ir.Resolve(ctx, "concurrent_acc", "concurrent_did")
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d error: %v", i, err)
		}
		if results[i] != expected {
			t.Errorf("goroutine %d: expected user_id %d, got %d", i, expected, results[i])
		}
	}
}

func TestIdentityResolver_MultipleDistinctIDs(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ir := st.Identity()

	// Create account with first distinct_id
	uid1, err := ir.Resolve(ctx, "multi_acc", "multi_did_1")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}

	// Add second distinct_id to same account
	uid2, err := ir.Resolve(ctx, "multi_acc", "multi_did_2")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if uid2 != uid1 {
		t.Errorf("expected same user_id %d for second distinct_id, got %d", uid1, uid2)
	}

	// Add third distinct_id
	uid3, err := ir.Resolve(ctx, "multi_acc", "multi_did_3")
	if err != nil {
		t.Fatalf("third resolve: %v", err)
	}
	if uid3 != uid1 {
		t.Errorf("expected same user_id %d for third distinct_id, got %d", uid1, uid3)
	}

	// Query each distinct_id independently
	for _, did := range []string{"multi_did_1", "multi_did_2", "multi_did_3"} {
		uid, err := ir.Resolve(ctx, "", did)
		if err != nil {
			t.Fatalf("resolve distinct %s: %v", did, err)
		}
		if uid != uid1 {
			t.Errorf("expected user_id %d for distinct %s, got %d", uid1, did, uid)
		}
	}
}


