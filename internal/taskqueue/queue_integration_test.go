package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const testMongoURI = "mongodb://localhost:27017"

func testQueue(t *testing.T) (*Queue, *Registry, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI).
		SetServerSelectionTimeout(2*time.Second))
	if err != nil {
		t.Skipf("mongo unavailable: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("mongo unavailable: %v", err)
	}
	dbName := fmt.Sprintf("tango_tq_test_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	db := client.Database(dbName)
	// Disable retry backoff by default so claim-semantics tests can re-claim a
	// retried task immediately; the backoff window is exercised separately.
	q := NewQueue(db.Collection("_tango_tasks")).WithRetryBackoff(0, 0)
	r := NewRegistry(db.Collection("_tango_instances"), 90*time.Second)
	if err := q.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("queue indexes: %v", err)
	}
	cleanup := func() {
		dropCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = db.Drop(dropCtx)
		_ = client.Disconnect(dropCtx)
	}
	return q, r, cleanup
}

func TestQueue_PublishAndClaimRandom(t *testing.T) {
	q, _, cleanup := testQueue(t)
	defer cleanup()
	ctx := context.Background()

	id, err := q.Publish(ctx, TaskSQL, map[string]any{"sql": "SELECT 1"}, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// Any instance can claim an untargeted task.
	task, err := q.Claim(ctx, "agent-1", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if task.ID != id || task.ClaimedBy != "agent-1" || task.Status != StatusClaimed {
		t.Errorf("unexpected claimed task: %+v", task)
	}

	// No more claimable tasks.
	if _, err := q.Claim(ctx, "agent-2", time.Minute); !errors.Is(err, ErrNoTask) {
		t.Errorf("expected ErrNoTask, got %v", err)
	}
}

func TestQueue_Targeting(t *testing.T) {
	q, _, cleanup := testQueue(t)
	defer cleanup()
	ctx := context.Background()

	_, err := q.Publish(ctx, TaskBackfill, nil, PublishOptions{Target: "agent-A"})
	if err != nil {
		t.Fatal(err)
	}

	// Wrong instance cannot claim it.
	if _, err := q.Claim(ctx, "agent-B", time.Minute); !errors.Is(err, ErrNoTask) {
		t.Errorf("agent-B should not claim agent-A's task, got %v", err)
	}
	// Target instance can.
	task, err := q.Claim(ctx, "agent-A", time.Minute)
	if err != nil {
		t.Fatalf("agent-A claim: %v", err)
	}
	if task.Target != "agent-A" {
		t.Errorf("target = %q", task.Target)
	}
}

func TestQueue_LeaseReclaim(t *testing.T) {
	q, _, cleanup := testQueue(t)
	defer cleanup()
	ctx := context.Background()

	_, _ = q.Publish(ctx, TaskSQL, nil, PublishOptions{MaxAttempts: 5})

	// agent-1 claims with a very short lease, then "dies".
	t1, err := q.Claim(ctx, "agent-1", 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	// Immediately, agent-2 cannot steal it (lease still valid).
	if _, err := q.Claim(ctx, "agent-2", time.Minute); !errors.Is(err, ErrNoTask) {
		t.Errorf("agent-2 stole a live-lease task")
	}
	// After the lease expires, agent-2 reclaims it.
	time.Sleep(80 * time.Millisecond)
	t2, err := q.Claim(ctx, "agent-2", time.Minute)
	if err != nil {
		t.Fatalf("agent-2 reclaim: %v", err)
	}
	if t2.ID != t1.ID || t2.ClaimedBy != "agent-2" {
		t.Errorf("reclaim mismatch: %+v", t2)
	}
	if t2.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", t2.Attempts)
	}

	// agent-1's renew must now fail (it lost the lease).
	if err := q.RenewLease(ctx, t1.ID, "agent-1", time.Minute); err == nil {
		t.Errorf("agent-1 renew should fail after reclaim")
	}
}

func TestQueue_CompleteAndFailRetry(t *testing.T) {
	q, _, cleanup := testQueue(t)
	defer cleanup()
	ctx := context.Background()

	id, _ := q.Publish(ctx, TaskSQL, nil, PublishOptions{MaxAttempts: 2})

	// First claim fails -> requeued (attempts 1 < max 2).
	task, _ := q.Claim(ctx, "a", time.Minute)
	if err := q.Fail(ctx, task, "a", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	got, _ := q.Get(ctx, id)
	if got.Status != StatusPending {
		t.Errorf("after first fail status = %q, want pending (retry)", got.Status)
	}

	// Second claim fails -> terminal (attempts 2 == max 2).
	task, _ = q.Claim(ctx, "a", time.Minute)
	if err := q.Fail(ctx, task, "a", errors.New("boom2")); err != nil {
		t.Fatal(err)
	}
	got, _ = q.Get(ctx, id)
	if got.Status != StatusFailed {
		t.Errorf("after second fail status = %q, want failed", got.Status)
	}
}

func TestQueue_MaxAttemptsBlocksClaim(t *testing.T) {
	q, _, cleanup := testQueue(t)
	defer cleanup()
	ctx := context.Background()

	_, _ = q.Publish(ctx, TaskSQL, nil, PublishOptions{MaxAttempts: 1})
	task, _ := q.Claim(ctx, "a", time.Minute)
	_ = q.Fail(ctx, task, "a", errors.New("x")) // terminal (1==1)

	if _, err := q.Claim(ctx, "b", time.Minute); !errors.Is(err, ErrNoTask) {
		t.Errorf("exhausted task should not be claimable, got %v", err)
	}
}

// TestQueue_ReapExhaustedClaimed reproduces B1: an agent claims a task up to
// maxAttempts times and "crashes" each time (never reporting), so the task is
// stuck in claimed with an expired lease. Reap must transition it to failed.
func TestQueue_ReapExhaustedClaimed(t *testing.T) {
	q, _, cleanup := testQueue(t)
	defer cleanup()
	ctx := context.Background()

	id, _ := q.Publish(ctx, TaskSQL, nil, PublishOptions{MaxAttempts: 2})

	// Two crash-claims with a tiny lease, never finished.
	for i := 0; i < 2; i++ {
		if _, err := q.Claim(ctx, "crasher", 20*time.Millisecond); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	time.Sleep(40 * time.Millisecond) // let the lease expire

	// Before reap: not claimable (attempts exhausted) and still 'claimed'.
	if _, err := q.Claim(ctx, "other", time.Minute); !errors.Is(err, ErrNoTask) {
		t.Fatalf("expected exhausted task not claimable, got %v", err)
	}
	got, _ := q.Get(ctx, id)
	if got.Status != StatusClaimed {
		t.Fatalf("pre-reap status = %q, want claimed (stuck)", got.Status)
	}

	n, err := q.Reap(ctx, nil, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
	got, _ = q.Get(ctx, id)
	if got.Status != StatusFailed {
		t.Errorf("post-reap status = %q, want failed", got.Status)
	}
}

// TestQueue_ReapOrphanTargeted reproduces B2: a targeted task whose target is
// offline and older than the grace window is failed by Reap.
func TestQueue_ReapOrphanTargeted(t *testing.T) {
	q, reg, cleanup := testQueue(t)
	defer cleanup()
	ctx := context.Background()

	id, _ := q.Publish(ctx, TaskSQL, nil, PublishOptions{Target: "ghost"})

	// grace = 0 so the just-created task is immediately eligible; target never
	// registered a heartbeat, so it is not alive.
	n, err := q.Reap(ctx, reg, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reaped %d, want 1", n)
	}
	got, _ := q.Get(ctx, id)
	if got.Status != StatusFailed {
		t.Errorf("orphan status = %q, want failed", got.Status)
	}
}

// TestQueue_FailRetryBackoffGate verifies a retried task is not re-claimable
// until its backoff window elapses (B4).
func TestQueue_FailRetryBackoffGate(t *testing.T) {
	q, _, cleanup := testQueue(t)
	defer cleanup()
	q.WithRetryBackoff(200*time.Millisecond, time.Second)
	ctx := context.Background()

	_, _ = q.Publish(ctx, TaskSQL, nil, PublishOptions{MaxAttempts: 5})
	task, _ := q.Claim(ctx, "a", time.Minute)
	if err := q.Fail(ctx, task, "a", errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	// Immediately: gated, not claimable.
	if _, err := q.Claim(ctx, "a", time.Minute); !errors.Is(err, ErrNoTask) {
		t.Errorf("expected backoff gate to block immediate re-claim, got %v", err)
	}
	time.Sleep(260 * time.Millisecond)
	if _, err := q.Claim(ctx, "a", time.Minute); err != nil {
		t.Errorf("after backoff window, claim should succeed: %v", err)
	}
}

func TestRegistry_HeartbeatAndAlive(t *testing.T) {
	_, r, cleanup := testQueue(t)
	defer cleanup()
	ctx := context.Background()

	alive, _ := r.IsAlive(ctx, "agent-X")
	if alive {
		t.Errorf("unknown instance reported alive")
	}
	if err := r.Heartbeat(ctx, Instance{ID: "agent-X", Hostname: "h1"}); err != nil {
		t.Fatal(err)
	}
	alive, _ = r.IsAlive(ctx, "agent-X")
	if !alive {
		t.Errorf("heart-beating instance not alive")
	}
	list, _ := r.ListAlive(ctx)
	if len(list) != 1 || list[0].ID != "agent-X" {
		t.Errorf("ListAlive = %+v", list)
	}
}
