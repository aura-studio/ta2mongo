package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aura-studio/tango/internal/logging"
)

// Fetcher pulls historical rows from the ThinkingData OpenAPI and emits them as
// TA JSON log lines through an injected sink (emit). It is deliberately
// dao-free, engine-free and parser-free: it only knows how to fetch and encode.
// The caller (Engine.RunBackfill) wires emit to an in-memory relay source
// (source/mem) that the normal upload pipeline drains — so backfilled rows flow
// through the same parse → filter → identity → write path as live ingestion,
// with no custom write model or checkpoint.
type Fetcher struct {
	cfg    *Config
	client *Client
}

// New builds a Fetcher with an HTTP client honouring cfg.Proxy and no
// wall-clock timeout (streaming bodies are bounded by the request context).
func New(cfg *Config) (*Fetcher, error) {
	httpC, err := NewHTTPClient(cfg.Proxy, 0)
	if err != nil {
		return nil, fmt.Errorf("backfill: build http client: %w", err)
	}
	return &Fetcher{cfg: cfg, client: NewClient(cfg.APIBaseURL, cfg.Token, httpC)}, nil
}

// Run fetches every unit (one per partition date for the event table, or the
// single user chunk) and calls emit for each encoded TA JSON log line, in
// order. Days are processed sequentially; per-page retries and task-expiry
// resubmits are handled internally. It returns the first fatal error (a submit
// failure, an exhausted page, or an emit error such as the sink being closed /
// ctx cancelled). There is no checkpoint: a re-run re-fetches, and the write
// models dedup by #uuid (events) / #user_id (user_setOnce).
func (f *Fetcher) Run(ctx context.Context, emit func(line string) error) error {
	days, err := f.cfg.Days()
	if err != nil {
		return fmt.Errorf("backfill: %w", err)
	}
	defaultType := f.cfg.defaultType()
	userKeys := f.userKeys()
	logging.WithFields(logging.Fields{
		"projectID": f.cfg.ProjectID,
		"table":     f.cfg.Table,
		"chunks":    len(days),
		"startDate": f.cfg.PartDateRange.Start,
		"endDate":   f.cfg.PartDateRange.End,
	}).Info("backfill: fetch starting")

	for _, day := range days {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := f.fetchDay(ctx, day, defaultType, userKeys, emit); err != nil {
			return fmt.Errorf("backfill: chunk %s: %w", day, err)
		}
		logging.WithField("chunk", day).Info("backfill: chunk fetched")
	}
	logging.Info("backfill: fetch complete")
	return nil
}

// userKeys returns the per-run #uuid/#time synthesis config for the user table
// (nil for the event table, whose rows carry their own #uuid/#time). The
// Fallback timestamp is stamped once per run so a user row missing both #time
// and the configured time column still parses.
func (f *Fetcher) userKeys() *UserKeys {
	if f.cfg.Table != TableUser {
		return nil
	}
	return &UserKeys{
		TimeColumn: f.cfg.UserTimeColumn,
		Fallback:   time.Now().Format("2006-01-02 15:04:05.000"),
	}
}

// fetchDay submits one day's SQL, polls to completion, and paginates — encoding
// each row and emitting it. On a task-expired error it re-submits a fresh task
// once and restarts pagination from page 0 (safe: write models dedup).
func (f *Fetcher) fetchDay(ctx context.Context, day, defaultType string, userKeys *UserKeys, emit func(line string) error) error {
	sql := f.cfg.BuildSQL(day)
	var taskID string
	resubmitted := false

attempt:
	if taskID == "" {
		id, err := f.client.SubmitSQL(ctx, sql, f.cfg.EffectivePageSize())
		if err != nil {
			return fmt.Errorf("submit: %w", err)
		}
		taskID = id
	}

	info, err := f.await(ctx, taskID)
	if err != nil {
		if errors.Is(err, ErrTaskExpired) && !resubmitted {
			logging.WithField("chunk", day).Warn("backfill: task expired; resubmitting")
			resubmitted, taskID = true, ""
			goto attempt
		}
		return err
	}

	logging.WithFields(logging.Fields{
		"chunk": day, "taskId": taskID,
		"pageCount": info.ResultStat.PageCount, "rowCount": info.ResultStat.RowCount,
	}).Info("backfill: task ready; paginating")

	for pageID := 0; pageID < info.ResultStat.PageCount; pageID++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := f.emitPage(ctx, taskID, pageID, info.ResultStat.Headers, defaultType, userKeys, emit); err != nil {
			if errors.Is(err, ErrTaskExpired) && !resubmitted {
				logging.WithField("chunk", day).Warn("backfill: task expired mid-paginate; resubmitting from page 0")
				resubmitted, taskID = true, ""
				goto attempt
			}
			return fmt.Errorf("page %d: %w", pageID, err)
		}
	}
	return nil
}

// await polls the task until FINISHED, FAILED, or PollTimeout elapses.
func (f *Fetcher) await(ctx context.Context, taskID string) (*TaskInfoResult, error) {
	deadline := time.Now().Add(f.cfg.PollTimeout)
	for {
		info, err := f.client.TaskInfo(ctx, taskID)
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
			return nil, fmt.Errorf("poll timeout (%s) exceeded for taskId=%s", f.cfg.PollTimeout, taskID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.cfg.PollInterval):
		}
	}
}

// emitPage streams one page into a fresh line buffer (encoding each row), then
// emits the whole page. Buffering before emit means a transient retry never
// double-emits rows (the partial buffer is discarded). ErrTaskExpired and ctx
// errors bubble up so fetchDay can resubmit.
func (f *Fetcher) emitPage(ctx context.Context, taskID string, pageID int, headers []string, defaultType string, userKeys *UserKeys, emit func(line string) error) error {
	attempts := f.cfg.PageRetries
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lines := make([]string, 0, 2048)
		err := f.client.StreamResultPage(ctx, taskID, pageID, f.cfg.PageSize,
			func(row []interface{}) error {
				line, encErr := EncodeRowAsJSONLine(headers, row, defaultType, userKeys)
				if encErr != nil {
					logging.WithError(encErr).Debug("backfill: encode row; skipping")
					return nil
				}
				lines = append(lines, line)
				return nil
			})
		if err == nil {
			for _, line := range lines {
				if e := emit(line); e != nil {
					return e
				}
			}
			return nil
		}
		if errors.Is(err, ErrTaskExpired) || ctx.Err() != nil {
			return err
		}
		lastErr = err
		if attempt < attempts {
			wait := time.Duration(attempt) * time.Second
			logging.WithError(err).WithFields(logging.Fields{
				"taskId": taskID, "pageId": pageID, "attempt": attempt, "retry_in": wait,
			}).Warn("backfill: page failed; retrying")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
	}
	return fmt.Errorf("page %d failed after %d attempts: %w", pageID, attempts, lastErr)
}
