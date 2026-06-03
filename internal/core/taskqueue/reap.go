package taskqueue

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

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
