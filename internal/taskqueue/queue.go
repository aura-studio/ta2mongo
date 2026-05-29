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

// Queue is the MongoDB-backed task queue.
type Queue struct {
	coll *mongo.Collection
}

// NewQueue wraps the given collection as a task queue.
func NewQueue(coll *mongo.Collection) *Queue { return &Queue{coll: coll} }

// EnsureIndexes creates the index that backs efficient claiming.
func (q *Queue) EnsureIndexes(ctx context.Context) error {
	_, err := q.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys: bson.D{
			{Key: "status", Value: 1},
			{Key: "target", Value: 1},
			{Key: "createdAt", Value: 1},
		},
	})
	return err
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
// claimable when it is pending, OR it is claimed but its lease has expired
// (the previous owner died) and it still has attempts left. Targeting:
// instanceID can claim tasks with target == "" or target == instanceID.
//
// Returns ErrNoTask when nothing is claimable.
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
		},
	}
	update := bson.M{
		"$set": bson.M{
			"status":     StatusClaimed,
			"claimedBy":  instanceID,
			"leaseUntil": now.Add(lease),
			"updatedAt":  now,
			"startedAt":  now,
		},
		"$inc": bson.M{"attempts": 1},
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

// Complete marks a task succeeded with an optional result payload.
func (q *Queue) Complete(ctx context.Context, taskID, instanceID string, result map[string]any) error {
	return q.finish(ctx, taskID, instanceID, StatusSucceeded, result, "")
}

// Fail marks a task failed. If it still has attempts remaining, it is instead
// returned to pending so another (or the same) agent can retry it.
func (q *Queue) Fail(ctx context.Context, task *Task, instanceID string, cause error) error {
	now := time.Now().UTC()
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	// Retry if attempts remain; else terminal failure.
	if task.Attempts < task.MaxAttempts {
		res, err := q.coll.UpdateOne(ctx,
			bson.M{"_id": task.ID, "claimedBy": instanceID, "status": StatusClaimed},
			bson.M{"$set": bson.M{
				"status":    StatusPending,
				"error":     msg,
				"updatedAt": now,
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
		bson.M{"_id": taskID, "claimedBy": instanceID},
		bson.M{"$set": set},
	)
	if err != nil {
		return fmt.Errorf("taskqueue: finish: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("taskqueue: cannot finish task %s (not owner)", taskID)
	}
	return nil
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
