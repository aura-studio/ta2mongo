package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"rocket-nano/tools/tango/config"
)

// ExecuteSQL runs one explicit SQL statement through the streaming import,
// routing rows to user/event per the runner's configured table. Unlike the
// day-chunked Run, it does not persist a checkpoint — the caller (e.g. the
// task queue) owns retry, and #uuid / #user_id upserts keep re-runs idempotent.
// Returns the number of rows imported.
//
// This is the execution path for `sql` tasks: the SQL is taken verbatim from
// the task payload rather than built from a date range.
func (r *Runner) ExecuteSQL(ctx context.Context, sql string) (int64, error) {
	taskID, err := r.client.SubmitSQL(ctx, sql, r.cfg.Backfill.EffectivePageSize())
	if err != nil {
		r.stats.HTTPErrors.Add(1)
		return 0, fmt.Errorf("submit: %w", err)
	}

	info, err := r.awaitFinished(ctx, taskID)
	if err != nil {
		return 0, err
	}
	r.logger.WithFields(logrus.Fields{
		"taskId":    taskID,
		"pageCount": info.ResultStat.PageCount,
		"rowCount":  info.ResultStat.RowCount,
	}).Info("backfill: sql task ready; starting pagination")

	if r.progress != nil {
		r.progress.SetCurrentChunk("sql")
	}

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
		if r.progress != nil {
			r.progress.SetPageInfo(pageID, info.ResultStat.PageCount)
		}
	}
	return total, nil
}

// resubmitDay clears the in-progress task state and starts the day over. The
// #uuid unique index keeps already-written rows safely deduped.
func (r *Runner) resubmitDay(ctx context.Context, day, sql string) error {
	taskID, err := r.client.SubmitSQL(ctx, sql, r.cfg.Backfill.EffectivePageSize())
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

// awaitFinished polls the task until it is FINISHED, FAILED, or the configured
// PollTimeout elapses.
func (r *Runner) awaitFinished(ctx context.Context, taskID string) (*TaskInfoResult, error) {
	deadline := time.Now().Add(r.cfg.Backfill.PollTimeout)
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
			// fall through
		default:
			return nil, fmt.Errorf("unknown task status %q", info.Status)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("poll timeout (%s) exceeded for taskId=%s", r.cfg.Backfill.PollTimeout, taskID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.cfg.Backfill.PollInterval):
		}
	}
}

// ingestPageWithRetry wraps ingestPage with bounded retries on transient
// errors. ErrTaskExpired is never retried here — it bubbles up so the caller
// can resubmit the whole task. Re-fetching a page is safe: #uuid / #user_id
// upserts dedup any rows that were already written before the failure.
func (r *Runner) ingestPageWithRetry(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	attempts := r.cfg.Backfill.PageRetries
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
			backoff := time.Duration(attempt) * time.Second
			r.logger.WithError(err).WithFields(logrus.Fields{
				"taskId": taskID, "pageId": pageID, "attempt": attempt, "retry_in": backoff,
			}).Warn("backfill: page failed; retrying")
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return 0, fmt.Errorf("page %d failed after %d attempts: %w", pageID, attempts, lastErr)
}

// ingestPage routes one chunk's result rows into Mongo. The event-table path
// goes through the standard parse → filter → identity → BulkWrite pipeline
// (one page at a time). The user-table path skips the parser (TA's
// v_user_<pid> uses fields like #user_operation / #active_time that the
// event-shaped parser rejects) AND streams rows directly off the HTTP
// response, flushing batches of userFlushBatch upserts so a mid-page network
// failure cannot lose more than one batch.
func (r *Runner) ingestPage(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	if r.cfg.BackfillFilter.Table == config.BackfillTableUser {
		return r.streamUserPage(ctx, taskID, pageID, headers)
	}
	return r.fetchAndIngestEventPage(ctx, taskID, pageID, headers)
}
