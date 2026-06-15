package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
)

// userFlushBatch is the number of user-table snapshot upserts the runner
// accumulates before issuing a BulkWrite.
const userFlushBatch = 1000

// eventPageBuffer caps the per-page line slice pre-allocation for the event path.
const eventPageBuffer = 2048

// ExecuteSQL runs one explicit SQL statement through the streaming import,
// routing rows to user/event per the configured table. Unlike the day-chunked
// Run it persists no checkpoint — the caller owns retry, and #uuid / #user_id
// upserts keep re-runs idempotent. It is the path for verbatim-SQL tasks.
// Returns the number of rows imported. Run and ExecuteSQL are mutually
// exclusive on a given Runner.
func (r *Runner) ExecuteSQL(ctx context.Context, sql string) (int64, error) {
	taskID, err := r.client.SubmitSQL(ctx, sql, r.cfg.EffectivePageSize())
	if err != nil {
		r.stats.HTTPErrors.Add(1)
		return 0, fmt.Errorf("submit: %w", err)
	}

	info, err := r.awaitFinished(ctx, taskID)
	if err != nil {
		return 0, err
	}
	logging.WithFields(logging.Fields{
		"taskId":    taskID,
		"pageCount": info.ResultStat.PageCount,
		"rowCount":  info.ResultStat.RowCount,
	}).Info("backfill: sql task ready; starting pagination")

	var total int64
	for pageID := 0; pageID < info.ResultStat.PageCount; pageID++ {
		select {
		case <-ctx.Done():
			return total, ctx.Err()
		default:
		}
		rows, err := r.ingestPageWithRetry(ctx, taskID, pageID, info.ResultStat.Headers)
		if err != nil {
			r.stats.HTTPErrors.Add(1)
			return total, fmt.Errorf("page %d: %w", pageID, err)
		}
		r.stats.Pages.Add(1)
		total += int64(rows)
	}
	return total, nil
}

// resubmitDay clears the in-progress task state and starts the day over. The
// #uuid / #user_id upserts keep already-written rows safely deduped.
func (r *Runner) resubmitDay(ctx context.Context, day, sql string) error {
	taskID, err := r.client.SubmitSQL(ctx, sql, r.cfg.EffectivePageSize())
	if err != nil {
		r.stats.HTTPErrors.Add(1)
		return fmt.Errorf("resubmit: %w", err)
	}
	p := r.checkpoint.Day(day)
	p.Status = DayInProgress
	p.TaskID = taskID
	p.PageID = -1
	p.PageCount = 0
	if err := r.checkpoint.SetDay(ctx, day, p); err != nil {
		return err
	}
	return r.runDay(ctx, day)
}

// awaitFinished polls the task until it is FINISHED, FAILED, or PollTimeout
// elapses.
func (r *Runner) awaitFinished(ctx context.Context, taskID string) (*TaskInfoResult, error) {
	deadline := time.Now().Add(r.cfg.PollTimeout)
	for {
		info, err := r.client.TaskInfo(ctx, taskID)
		if err != nil {
			return nil, err
		}
		switch info.Status {
		case StatusFinished:
			return info, nil
		case StatusFailed:
			return nil, fmt.Errorf("ta task failed (taskId=%s)", taskID)
		case StatusRunning:
			// keep polling
		default:
			return nil, fmt.Errorf("unknown task status %q", info.Status)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("poll timeout (%s) exceeded for taskId=%s", r.cfg.PollTimeout, taskID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.cfg.PollInterval):
		}
	}
}

// ingestPageWithRetry wraps ingestPage with bounded retries on transient
// errors. ErrTaskExpired and ctx errors are never retried here — they bubble up
// so the caller can resubmit. Re-fetching a page is safe: upserts dedup rows
// written before the failure.
func (r *Runner) ingestPageWithRetry(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	attempts := r.cfg.PageRetries
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		rows, err := r.ingestPage(ctx, taskID, pageID, headers)
		if err == nil {
			return rows, nil
		}
		if errors.Is(err, ErrTaskExpired) || ctx.Err() != nil {
			return rows, err // not retryable here
		}
		lastErr = err
		if attempt < attempts {
			wait := time.Duration(attempt) * time.Second
			logging.WithError(err).WithFields(logging.Fields{
				"taskId": taskID, "pageId": pageID, "attempt": attempt, "retry_in": wait,
			}).Warn("backfill: page failed; retrying")
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	return 0, fmt.Errorf("page %d failed after %d attempts: %w", pageID, attempts, lastErr)
}

// ingestPage routes one page's rows into Mongo. The event-table path encodes
// rows to JSON log lines and feeds them through the engine's upload pipeline
// (parse → filter → identity → document-form write). The user-table path skips
// the parser (TA's v_user_<pid> uses fields the event parser rejects) and
// streams snapshot upserts directly.
func (r *Runner) ingestPage(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	if r.cfg.Table == TableUser {
		return r.streamUserPage(ctx, taskID, pageID, headers)
	}
	return r.ingestEventPage(ctx, taskID, pageID, headers)
}

// ingestEventPage streams a page of event rows, encodes each to a TA JSON log
// line, and feeds the whole page through the engine's upload pipeline. The
// engine's reporting filter and identity resolution apply; the backfill
// selection filter is already pushed down to the TA SQL (BackfillWhere).
func (r *Runner) ingestEventPage(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	lines := make([]string, 0, eventPageBuffer)
	rows := 0
	streamErr := r.client.StreamResultPage(ctx, taskID, pageID, r.cfg.PageSize,
		func(row []interface{}) error {
			rows++
			line, err := EncodeRowAsJSONLine(headers, row)
			if err != nil {
				r.stats.ParseErrors.Add(1)
				logging.WithError(err).Debug("backfill: encode row")
				return nil
			}
			lines = append(lines, line)
			return nil
		})
	if len(lines) > 0 {
		res, upErr := r.upload(ctx, lines)
		r.stats.TotalLines.Add(res.Lines)
		r.stats.EventWrites.Add(res.EventWrites)
		r.stats.UserWrites.Add(res.UserWrites)
		r.stats.DeadLetters.Add(res.DeadLetters)
		r.stats.Filtered.Add(res.Filtered)
		if upErr != nil {
			r.stats.WriteErrors.Add(1)
			if streamErr == nil {
				streamErr = fmt.Errorf("event upload: %w", upErr)
			}
		}
	}
	return rows, streamErr
}

// streamUserPage streams a user-table result page directly into Mongo as
// snapshot upserts, flushing in batches of userFlushBatch. It bypasses the
// event parser and applies the backfill selection filter inline (unless
// SkipLocalFilter); #user_id upserts keep retries idempotent.
func (r *Runner) streamUserPage(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	userIDIdx := indexOf(headers, "#user_id")
	if userIDIdx < 0 {
		return 0, fmt.Errorf("user-table backfill: #user_id column not present in result headers")
	}
	skip := r.cfg.ForceSkip()
	coll := r.dao.Store.UserCollection()

	batch := make([]mongo.WriteModel, 0, userFlushBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := r.dao.Store.BulkWriteOrdered(ctx, coll, batch); err != nil {
			r.stats.WriteErrors.Add(1)
			return fmt.Errorf("user snapshot write: %w", err)
		}
		batch = batch[:0]
		return nil
	}

	rows := 0
	err := r.client.StreamResultPage(ctx, taskID, pageID, r.cfg.PageSize,
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

			if !r.cfg.SkipLocalFilter && !r.filter.Empty() {
				env := map[string]any{"#user_id": userID}
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

			batch = append(batch, dao.UserSnapshotWriteModel(userID, doc, skip))
			r.stats.UserWrites.Add(1)
			r.stats.ParsedOK.Add(1)
			if len(batch) >= userFlushBatch {
				return flush()
			}
			return nil
		})
	// Flush leftovers even on stream error so received rows land and dedup on retry.
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
