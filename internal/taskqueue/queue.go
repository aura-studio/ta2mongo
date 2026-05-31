package taskqueue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrNoTask is returned by Claim when there is no claimable task.
var ErrNoTask = errors.New("taskqueue: no claimable task")

// Default retry-backoff and retention parameters.
const (
	// defaultRetryBase is the first retry delay; it doubles each attempt up to
	// defaultRetryCap. Gates re-claim of a failed-with-retry task (B4).
	defaultRetryBase = 2 * time.Second
	defaultRetryCap  = 5 * time.Minute
	// defaultRetention is how long terminal (succeeded/failed) tasks are kept
	// before the TTL monitor deletes them (B2 unbounded-growth fix).
	defaultRetention = 7 * 24 * time.Hour
)

// Queue is the MongoDB-backed task queue.
type Queue struct {
	coll      *mongo.Collection
	retryBase time.Duration
	retryCap  time.Duration
	retention time.Duration
}

// NewQueue wraps the given collection as a task queue.
func NewQueue(coll *mongo.Collection) *Queue {
	return &Queue{coll: coll, retryBase: defaultRetryBase, retryCap: defaultRetryCap, retention: defaultRetention}
}

// WithRetryBackoff overrides the retry backoff window (base, cap). A zero base
// disables backoff, making a retried task immediately re-claimable (used in
// tests that assert claim semantics rather than timing).
func (q *Queue) WithRetryBackoff(base, cap time.Duration) *Queue {
	q.retryBase, q.retryCap = base, cap
	return q
}

// WithRetention overrides the terminal-task TTL.
func (q *Queue) WithRetention(d time.Duration) *Queue { q.retention = d; return q }

// backoff returns the delay before a task that has been attempted n times may
// be claimed again: retryBase * 2^(n-1), capped at retryCap.
func (q *Queue) backoff(attempts int) time.Duration {
	if q.retryBase <= 0 || attempts <= 0 {
		return 0
	}
	d := q.retryBase
	for i := 1; i < attempts; i++ {
		d *= 2
		if q.retryCap > 0 && d >= q.retryCap {
			return q.retryCap
		}
	}
	if q.retryCap > 0 && d > q.retryCap {
		return q.retryCap
	}
	return d
}

// EnsureIndexes creates the claim index plus a TTL index that deletes terminal
// tasks once their finishedAt is older than the retention window.
func (q *Queue) EnsureIndexes(ctx context.Context) error {
	if _, err := q.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "status", Value: 1},
			{Key: "target", Value: 1},
			{Key: "createdAt", Value: 1},
		},
	}); err != nil {
		return err
	}
	_, err := q.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "finishedAt", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(int32(q.retention.Seconds())),
	})
	// A pre-existing index with a different TTL is not fatal for operation.
	if err != nil && !isIndexConflict(err) {
		return err
	}
	return nil
}

// PublishOptions configures a published task.
type PublishOptions struct {
	// ID overrides the generated task id (optional; useful for idempotent
	// publishing).
	ID string
	// Target restricts execution to a named instance; "" = any.
	Target string
	// MaxAttempts caps reclaim retries; 0 means a sane default (3).
	MaxAttempts int
}

// Publish inserts a new pending task and returns its id.
func (q *Queue) Publish(ctx context.Context, typ TaskType, payload map[string]any, opts PublishOptions) (string, error) {
	id := opts.ID
	if id == "" {
		id = uuid.NewString()
	}
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	now := time.Now().UTC()
	task := Task{
		ID:          id,
		Type:        typ,
		Payload:     payload,
		Target:      opts.Target,
		Status:      StatusPending,
		MaxAttempts: maxAttempts,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if _, err := q.coll.InsertOne(ctx, task); err != nil {
		return "", fmt.Errorf("taskqueue: publish: %w", err)
	}
	return id, nil
}

// Claim atomically claims one task for instanceID and returns it. A task is
// claimable when:
//   - it is pending, OR it is claimed but its lease has expired (previous owner
//     died) and it still has attempts left;
//   - it has attempts remaining (attempts < maxAttempts);
//   - its retry-backoff gate has elapsed (notBefore <= now);
//   - it targets "" or this instance.
//
// startedAt is preserved across reclaims. Returns ErrNoTask when nothing is
// claimable.
func (q *Queue) Claim(ctx context.Context, instanceID string, lease time.Duration) (*Task, error) {
	now := time.Now().UTC()
	filter := bson.M{
		"$and": []bson.M{
			{"$or": []bson.M{
				{"target": ""},
				{"target": instanceID},
			}},
			{"$or": []bson.M{
				{"status": StatusPending},
				// Reclaim a dead owner's task once its lease has expired.
				{"status": StatusClaimed, "leaseUntil": bson.M{"$lt": now}},
			}},
			{"$expr": bson.M{"$lt": bson.A{"$attempts", "$maxAttempts"}}},
			{"$or": []bson.M{
				{"notBefore": bson.M{"$exists": false}},
				{"notBefore": bson.M{"$lte": now}},
			}},
		},
	}
	// Pipeline update so startedAt is set only on the first claim ($ifNull) and
	// attempts is incremented atomically.
	update := mongo.Pipeline{
		bson.D{{Key: "$set", Value: bson.D{
			{Key: "status", Value: StatusClaimed},
			{Key: "claimedBy", Value: instanceID},
			{Key: "leaseUntil", Value: now.Add(lease)},
			{Key: "updatedAt", Value: now},
			{Key: "startedAt", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$startedAt", now}}}},
			{Key: "attempts", Value: bson.D{{Key: "$add", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{"$attempts", 0}}}, 1}}}},
		}}},
	}
	opts := options.FindOneAndUpdate().
		SetSort(bson.D{{Key: "createdAt", Value: 1}}). // FIFO
		SetReturnDocument(options.After)

	var task Task
	err := q.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&task)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, ErrNoTask
	}
	if err != nil {
		return nil, fmt.Errorf("taskqueue: claim: %w", err)
	}
	return &task, nil
}

// RenewLease extends the lease of a task the caller owns. It only succeeds
// while the caller is still the claimant, guarding against renewing a task
// that was already reclaimed.
func (q *Queue) RenewLease(ctx context.Context, taskID, instanceID string, lease time.Duration) error {
	now := time.Now().UTC()
	res, err := q.coll.UpdateOne(ctx,
		bson.M{"_id": taskID, "claimedBy": instanceID, "status": StatusClaimed},
		bson.M{"$set": bson.M{"leaseUntil": now.Add(lease), "updatedAt": now}},
	)
	if err != nil {
		return fmt.Errorf("taskqueue: renew lease: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("taskqueue: lease lost for task %s (reclaimed by another agent?)", taskID)
	}
	return nil
}

// Complete marks a task succeeded with an optional result payload. It only
// succeeds while the caller still holds a valid lease, so a stale owner whose
// lease has lapsed (and whose task may have been reclaimed) cannot finalize it.
func (q *Queue) Complete(ctx context.Context, taskID, instanceID string, result map[string]any) error {
	return q.finish(ctx, taskID, instanceID, StatusSucceeded, result, "")
}

// Fail marks a task failed. If it still has attempts remaining, it is instead
// returned to pending with a backoff gate (notBefore) so the same agent does
// not immediately re-claim and hammer a deterministically-failing task. Like
// Complete, it requires the caller to still hold a valid lease.
func (q *Queue) Fail(ctx context.Context, task *Task, instanceID string, cause error) error {
	now := time.Now().UTC()
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	// Retry if attempts remain; else terminal failure.
	if task.Attempts < task.MaxAttempts {
		res, err := q.coll.UpdateOne(ctx,
			bson.M{"_id": task.ID, "claimedBy": instanceID, "status": StatusClaimed, "leaseUntil": bson.M{"$gte": now}},
			bson.M{"$set": bson.M{
				"status":    StatusPending,
				"error":     msg,
				"updatedAt": now,
				"notBefore": now.Add(q.backoff(task.Attempts)),
			}, "$unset": bson.M{"leaseUntil": "", "claimedBy": ""}},
		)
		if err != nil {
			return fmt.Errorf("taskqueue: fail(retry): %w", err)
		}
		if res.MatchedCount == 0 {
			return fmt.Errorf("taskqueue: cannot requeue task %s (lease lost)", task.ID)
		}
		return nil
	}
	return q.finish(ctx, task.ID, instanceID, StatusFailed, nil, msg)
}

func (q *Queue) finish(ctx context.Context, taskID, instanceID string, status TaskStatus, result map[string]any, errMsg string) error {
	now := time.Now().UTC()
	set := bson.M{"status": status, "updatedAt": now, "finishedAt": now}
	if result != nil {
		set["result"] = result
	}
	if errMsg != "" {
		set["error"] = errMsg
	}
	res, err := q.coll.UpdateOne(ctx,
		bson.M{"_id": taskID, "claimedBy": instanceID, "leaseUntil": bson.M{"$gte": now}},
		bson.M{"$set": set},
	)
	if err != nil {
		return fmt.Errorf("taskqueue: finish: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("taskqueue: cannot finish task %s (not owner or lease lost)", taskID)
	}
	return nil
}

// Reap performs queue maintenance, returning the number of tasks transitioned
// to a terminal state. It fixes two stuck-task classes:
//
//   - A claimed task whose lease has expired and whose attempts are exhausted
//     (the owner crashed maxAttempts times, never reporting failure): it would
//     otherwise be neither reclaimable nor terminal — Reap marks it failed (B1).
//   - A pending task targeted at an instance that is no longer alive and that
//     was created more than offlineGrace ago: it would wait forever for an
//     agent that will never return — Reap marks it failed (B2).
//
// reg may be nil to skip the targeted-orphan sweep.
func (q *Queue) Reap(ctx context.Context, reg *Registry, offlineGrace time.Duration) (int, error) {
	now := time.Now().UTC()
	total := 0

	res, err := q.coll.UpdateMany(ctx,
		bson.M{
			"status":     StatusClaimed,
			"leaseUntil": bson.M{"$lt": now},
			"$expr":      bson.M{"$gte": bson.A{"$attempts", "$maxAttempts"}},
		},
		bson.M{"$set": bson.M{
			"status":     StatusFailed,
			"error":      "lease expired after exhausting attempts (agent crashed)",
			"updatedAt":  now,
			"finishedAt": now,
		}, "$unset": bson.M{"leaseUntil": "", "claimedBy": ""}},
	)
	if err != nil {
		return total, fmt.Errorf("taskqueue: reap(exhausted): %w", err)
	}
	total += int(res.ModifiedCount)

	if reg == nil {
		return total, nil
	}

	// Targeted-orphan sweep: pending targeted tasks older than the grace window
	// whose target instance is no longer alive.
	cutoff := now.Add(-offlineGrace)
	cur, err := q.coll.Find(ctx, bson.M{
		"status":    StatusPending,
		"target":    bson.M{"$ne": ""},
		"createdAt": bson.M{"$lt": cutoff},
	})
	if err != nil {
		return total, fmt.Errorf("taskqueue: reap(orphan): %w", err)
	}
	var pending []Task
	if err := cur.All(ctx, &pending); err != nil {
		return total, fmt.Errorf("taskqueue: reap(orphan) decode: %w", err)
	}
	for _, t := range pending {
		alive, err := reg.IsAlive(ctx, t.Target)
		if err != nil {
			return total, err
		}
		if alive {
			continue
		}
		r, err := q.coll.UpdateOne(ctx,
			bson.M{"_id": t.ID, "status": StatusPending},
			bson.M{"$set": bson.M{
				"status":     StatusFailed,
				"error":      fmt.Sprintf("target instance %q offline past grace period", t.Target),
				"updatedAt":  now,
				"finishedAt": now,
			}},
		)
		if err != nil {
			return total, fmt.Errorf("taskqueue: reap(orphan) update: %w", err)
		}
		total += int(r.ModifiedCount)
	}
	return total, nil
}

// Get fetches a task by id (nil if absent).
func (q *Queue) Get(ctx context.Context, taskID string) (*Task, error) {
	var task Task
	err := q.coll.FindOne(ctx, bson.M{"_id": taskID}).Decode(&task)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("taskqueue: get: %w", err)
	}
	return &task, nil
}
