package backfill

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/internal/core/store"
)

// userFlushBatch is the number of user-table snapshot upserts the runner
// accumulates before issuing a BulkWrite. Sized to cap per-flush latency
// while keeping the per-row overhead reasonable.
const userFlushBatch = 1000

// streamUserPage streams a user-table result page directly into Mongo as
// snapshot upserts, flushing in batches of userFlushBatch. It bypasses the
// event parser (the v_user_<pid> shape is not event-envelope) and applies the
// backfill selection filter inline; #user_id upserts keep retries idempotent.
func (r *Runner) streamUserPage(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	userIDIdx := indexOf(headers, "#user_id")
	if userIDIdx < 0 {
		return 0, fmt.Errorf("user-table backfill: #user_id column not present in result headers")
	}
	skip := r.cfg.Backfill.ForceSkip()
	coll := r.store.UserCollection()

	batch := make([]mongo.WriteModel, 0, userFlushBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := r.store.BulkWriteOrdered(ctx, coll, batch); err != nil {
			r.stats.WriteErrors.Add(1)
			return fmt.Errorf("user snapshot write: %w", err)
		}
		batch = batch[:0]
		return nil
	}

	rows := 0
	err := r.client.StreamResultPage(ctx, taskID, pageID, r.cfg.Backfill.PageSize,
		func(row []interface{}) error {
			rows++
			r.stats.TotalLines.Add(1)
			if len(row) != len(headers) {
				r.stats.ParseErrors.Add(1)
				r.stats.DeadLetters.Add(1)
				return nil
			}
			userID := row[userIDIdx]
			if userID == nil {
				r.stats.ParseErrors.Add(1)
				r.stats.DeadLetters.Add(1)
				return nil
			}

			doc := bson.M{}
			for i, h := range headers {
				if i == userIDIdx {
					continue
				}
				if v := row[i]; v != nil {
					doc[h] = v
				}
			}

			if !r.cfg.Backfill.SkipLocalFilter && !r.filter.Empty() {
				env := bson.M{"#user_id": userID}
				for k, v := range doc {
					env[k] = v
				}
				keep, ferr := r.filter.Keep(env)
				if ferr != nil {
					r.stats.FilterErrors.Add(1)
				}
				if !keep {
					r.stats.Filtered.Add(1)
					return nil
				}
			}

			batch = append(batch, store.UserSnapshotWriteModel(userID, doc, skip))
			r.stats.UserWrites.Add(1)
			r.stats.ParsedOK.Add(1)
			if len(batch) >= userFlushBatch {
				return flush()
			}
			return nil
		})
	// Flush whatever is left even if the stream errored, so the rows we did
	// receive land in Mongo and can be skipped by upsert on the next attempt.
	if fErr := flush(); fErr != nil && err == nil {
		err = fErr
	}
	return rows, err
}

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}
