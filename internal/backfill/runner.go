package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser/filter"
)

// UploadStats mirrors the engine Result fields the runner accumulates from the
// event path. It is kept local so internal/backfill does not import
// internal/role/api (which imports this package — that would be a cycle).
type UploadStats struct {
	Lines       int64
	UserWrites  int64
	EventWrites int64
	DeadLetters int64
	Filtered    int64
}

// EventUploader ingests one page of decoded TA log lines through the engine's
// upload pipeline (parse → filter → identity → DocumentDB-safe write) and
// returns the per-call stats. Engine.RunBackfill supplies this, adapting
// Engine.Upload; the runner never imports the api package.
type EventUploader func(ctx context.Context, lines []string) (UploadStats, error)

// Runner orchestrates one backfill execution. It does not own the Mongo
// connection (the engine does); it borrows the dao for the user-snapshot path
// and the checkpoint collection, and feeds the event path through upload.
type Runner struct {
	cfg        *Config
	dao        *dao.Dao
	upload     EventUploader
	filter     *filter.Holder
	client     *Client
	checkpoint *Checkpoint
	stats      Stats
}

// NewRunner builds a ready-to-run Runner with a day-chunk checkpoint. The dao's
// Mongo connection and Store are borrowed, not owned — the caller (the engine)
// owns their lifecycle. upload routes the event path; the user path writes
// snapshot upserts through d.Store directly.
func NewRunner(ctx context.Context, cfg *Config, d *dao.Dao, upload EventUploader) (*Runner, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("backfill: %w", err)
	}
	httpC, err := NewHTTPClient(cfg.Proxy, 0)
	if err != nil {
		return nil, fmt.Errorf("backfill: build http client: %w", err)
	}

	r := &Runner{
		cfg:    cfg,
		dao:    d,
		upload: upload,
		filter: filter.NewHolder(flt),
		client: NewClient(cfg.APIBaseURL, cfg.Token, httpC),
	}

	filterWhere, err := cfg.BackfillWhere()
	if err != nil {
		return nil, fmt.Errorf("backfill: %w", err)
	}
	sig := SQLSignature(cfg.Table, cfg.ProjectID, filterWhere,
		cfg.EventTimeRange.Start, cfg.EventTimeRange.End)

	startDate, endDate := cfg.PartDateRange.Start, cfg.PartDateRange.End
	if cfg.Table == TableUser {
		// User tables are not partitioned; collapse to a single virtual "day"
		// so the day-keyed checkpoint logic carries over unchanged.
		startDate, endDate = UserChunkKey, UserChunkKey
	}

	cp, err := NewCheckpoint(ctx, d.Mongo.DB.Collection(cfg.ProgressCollection),
		cfg.RunID, cfg.APIBaseURL, cfg.ProjectID, cfg.Table, startDate, endDate, sig)
	if err != nil {
		return nil, fmt.Errorf("backfill: %w", err)
	}
	r.checkpoint = cp
	return r, nil
}

// EnsureIndexes ensures the standard collection indexes exist. The checkpoint
// collection is keyed by _id and needs no further index.
func (r *Runner) EnsureIndexes(ctx context.Context) error {
	return r.dao.Store.EnsureIndexes(ctx)
}

// Stats exposes the collected counters (live during Run).
func (r *Runner) Stats() *Stats { return &r.stats }

// Result snapshots the run's counters into the cross-layer UploadStats shape so
// Engine.RunBackfill can map it onto api.Result.
func (r *Runner) Result() UploadStats {
	s := r.stats.Counters.Snapshot()
	return UploadStats{
		Lines:       s.TotalLines,
		UserWrites:  s.UserWrites,
		EventWrites: s.EventWrites,
		DeadLetters: s.DeadLetters,
		Filtered:    s.Filtered,
	}
}

// Run drives the backfill: enumerate pending days, and for each submit → poll →
// paginate → ingest. Days are processed sequentially; a failed day is marked
// and skipped (not fatal). Context cancellation aborts.
func (r *Runner) Run(ctx context.Context) error {
	days := r.checkpoint.PendingDays()
	if len(days) == 0 {
		logging.Info("backfill: no pending chunks; nothing to do")
		return nil
	}

	logging.WithFields(logging.Fields{
		"runID":     r.cfg.RunID,
		"projectID": r.cfg.ProjectID,
		"table":     r.cfg.Table,
		"chunks":    len(days),
		"startDate": r.cfg.PartDateRange.Start,
		"endDate":   r.cfg.PartDateRange.End,
	}).Info("backfill: starting")

	// Periodic progress logging replaces the v1.0 terminal ProgressBar.
	stop := make(chan struct{})
	progressDone := r.startProgressLogger(stop)
	defer func() {
		close(stop)
		<-progressDone
	}()

	for _, day := range days {
		select {
		case <-ctx.Done():
			logging.Warn("backfill: context cancelled, exiting")
			return ctx.Err()
		default:
		}

		if err := r.runDay(ctx, day); err != nil {
			r.stats.DaysFailed.Add(1)
			if markErr := r.checkpoint.MarkFailed(ctx, day, err); markErr != nil {
				logging.WithError(markErr).Warn("backfill: mark chunk failed")
			}
			logging.WithError(err).WithField("chunk", day).Error("backfill: chunk failed; continuing")
			continue
		}

		r.stats.DaysCompleted.Add(1)
		logging.WithField("chunk", day).Info("backfill: chunk completed")
	}

	r.printSummary()
	return nil
}

// runDay performs one day's submit → poll → paginate → ingest cycle, resuming
// from any saved (taskId, pageId) state in the checkpoint.
func (r *Runner) runDay(ctx context.Context, day string) error {
	progress := r.checkpoint.Day(day)
	sql := r.buildDaySQL(day)

	// Acquire a valid taskId, either resuming an existing one or submitting fresh.
	if progress.TaskID == "" || progress.Status == DayPending || progress.Status == DayFailed {
		taskID, err := r.client.SubmitSQL(ctx, sql, r.cfg.EffectivePageSize())
		if err != nil {
			r.stats.HTTPErrors.Add(1)
			return fmt.Errorf("submit: %w", err)
		}
		progress = DayProgress{Status: DayInProgress, TaskID: taskID, PageID: -1}
		if err := r.checkpoint.SetDay(ctx, day, progress); err != nil {
			return err
		}
	}

	// Wait for the task to finish, with timeout.
	info, err := r.awaitFinished(ctx, progress.TaskID)
	if err != nil {
		if errors.Is(err, ErrTaskExpired) {
			logging.WithField("day", day).Warn("backfill: task expired; resubmitting")
			return r.resubmitDay(ctx, day, sql)
		}
		return err
	}

	progress.PageCount = info.ResultStat.PageCount
	if err := r.checkpoint.SetDay(ctx, day, progress); err != nil {
		return err
	}
	logging.WithFields(logging.Fields{
		"chunk":     day,
		"taskId":    progress.TaskID,
		"pageCount": info.ResultStat.PageCount,
		"rowCount":  info.ResultStat.RowCount,
	}).Info("backfill: task ready; starting pagination")

	// Paginate from the next unprocessed page.
	startPage := progress.PageID + 1
	for pageID := startPage; pageID < progress.PageCount; pageID++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rows, err := r.ingestPageWithRetry(ctx, progress.TaskID, pageID, info.ResultStat.Headers)
		if err != nil {
			if errors.Is(err, ErrTaskExpired) {
				logging.WithField("day", day).Warn("backfill: task expired mid-paginate; resubmitting from page 0")
				return r.resubmitDay(ctx, day, sql)
			}
			r.stats.HTTPErrors.Add(1)
			return fmt.Errorf("page %d: %w", pageID, err)
		}

		r.stats.Pages.Add(1)
		progress.PageID = pageID
		progress.Rows += int64(rows)
		if err := r.checkpoint.SetDay(ctx, day, progress); err != nil {
			return fmt.Errorf("checkpoint flush: %w", err)
		}
	}

	return r.checkpoint.MarkCompleted(ctx, day, progress.Rows)
}

// startProgressLogger ticks a periodic stats line until stop is closed,
// returning a channel closed when the goroutine exits.
func (r *Runner) startProgressLogger(stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s := r.stats.Counters.Snapshot()
				logging.WithFields(logging.Fields{
					"days_completed": r.stats.DaysCompleted.Load(),
					"pages":          r.stats.Pages.Load(),
					"lines":          s.TotalLines,
					"event_writes":   s.EventWrites,
					"user_writes":    s.UserWrites,
				}).Info("backfill: progress")
			}
		}
	}()
	return done
}

func (r *Runner) printSummary() {
	s := r.stats.Counters.Snapshot()
	logging.WithFields(logging.Fields{
		"days_completed":  r.stats.DaysCompleted.Load(),
		"days_failed":     r.stats.DaysFailed.Load(),
		"pages":           r.stats.Pages.Load(),
		"http_errors":     r.stats.HTTPErrors.Load(),
		"total_lines":     s.TotalLines,
		"parsed_ok":       s.ParsedOK,
		"parse_errors":    s.ParseErrors,
		"identity_errors": s.IdentityErrors,
		"user_writes":     s.UserWrites,
		"event_writes":    s.EventWrites,
		"dead_letters":    s.DeadLetters,
		"write_errors":    s.WriteErrors,
		"filtered":        s.Filtered,
		"filter_errors":   s.FilterErrors,
	}).Info("backfill: ========== summary ==========")
}
