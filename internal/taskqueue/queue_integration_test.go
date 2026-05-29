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
	q := NewQueue(db.Collection("_tango_tasks"))
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
