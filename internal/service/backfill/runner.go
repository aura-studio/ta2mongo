package backfill

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/filter"
	"rocket-nano/tools/tango/internal/core/runtime"
	"rocket-nano/tools/tango/internal/core/store"
	"rocket-nano/tools/tango/internal/core/talog"
)

// Runner orchestrates one backfill execution.
type Runner struct {
	cfg        config.Config
	logger     *logrus.Logger
	store      *store.Store
	parser     *talog.Parser
	filter     *filter.Holder
	client     *Client
	mongo      *runtime.MongoResource
	checkpoint *Checkpoint
	progress   *ProgressBar
	stats      Stats
}

// New connects to MongoDB and constructs a ready-to-run Runner with a
// day-chunk checkpoint (used by Run). The caller must call Shutdown after Run
// returns.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Runner, error) {
	r, db, err := newBase(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}

	filterWhere, err := cfg.BackfillWhere()
	if err != nil {
		_ = r.mongo.Close()
		return nil, fmt.Errorf("backfill: %w", err)
	}
	sig := SQLSignature(cfg.BackfillFilter.Table, cfg.Backfill.ProjectID, filterWhere,
		cfg.Backfill.EventTimeRange.Start, cfg.Backfill.EventTimeRange.End)

	startDate, endDate := cfg.Backfill.PartDateRange.Start, cfg.Backfill.PartDateRange.End
	if cfg.BackfillFilter.Table == config.BackfillTableUser {
		// User tables are not partitioned; collapse to a single virtual "day"
		// so the existing day-keyed checkpoint logic carries over unchanged.
		startDate, endDate = UserChunkKey, UserChunkKey
	}

	cp, err := NewCheckpoint(ctx, db.Collection(cfg.Backfill.ProgressCollection),
		cfg.Backfill.RunID, cfg.Backfill.APIBaseURL, cfg.Backfill.ProjectID,
		cfg.BackfillFilter.Table, startDate, endDate, sig)
	if err != nil {
		_ = r.mongo.Close()
		return nil, fmt.Errorf("backfill: %w", err)
	}
	r.checkpoint = cp
	return r, nil
}

// NewExecutor builds a Runner without a checkpoint, for one-shot ExecuteSQL
// runs (e.g. agent `sql` tasks). Run must not be called on it.
func NewExecutor(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Runner, error) {
	r, _, err := newBase(ctx, cfg, logger)
	return r, err
}

// newBase performs the shared connection + component wiring used by both New
// and NewExecutor. It returns the runner (with checkpoint left nil) and the
// database handle for the caller to finish setup.
func newBase(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Runner, *mongo.Database, error) {
	flt, err := cfg.BuildBackfillFilter()
	if err != nil {
		return nil, nil, fmt.Errorf("backfill: %w", err)
	}

	res, err := runtime.ConnectMongo(ctx, cfg.Mongo)
	if err != nil {
		return nil, nil, fmt.Errorf("backfill: %w", err)
	}
	db := res.DB
	st := runtime.NewStore(db, cfg, logger)

	httpC, err := NewHTTPClient(cfg.Backfill.Proxy, 0)
	if err != nil {
		_ = res.Close()
		return nil, nil, fmt.Errorf("backfill: build http client: %w", err)
	}

	return &Runner{
		cfg:    cfg,
		logger: logger,
		store:  st,
		parser: talog.NewParser(),
		filter: filter.NewHolder(flt),
		client: NewClient(cfg.Backfill.APIBaseURL, cfg.Backfill.Token, httpC),
		mongo:  res,
	}, db, nil
}

// Shutdown disconnects from MongoDB.
func (r *Runner) Shutdown() error { return r.mongo.Close() }

// EnsureIndexes ensures the standard collection indexes (event/user/dead_letter
// + id_mapping) exist. The checkpoint collection is keyed by _id and needs no
// further index.
func (r *Runner) EnsureIndexes(ctx context.Context) error {
	return r.store.EnsureIndexes(ctx)
}

// Stats exposes the collected counters (live during Run).
func (r *Runner) Stats() *Stats { return &r.stats }

// Run drives the backfill: enumerate pending days, for each day submit→poll→
// paginate and feed lines into the worker pipeline. Days are processed
// sequentially.
func (r *Runner) Run(ctx context.Context) error {
	days := r.checkpoint.PendingDays()
	if len(days) == 0 {
		r.logger.Info("backfill: no pending chunks; nothing to do")
		return nil
	}

	r.logger.WithFields(logrus.Fields{
		"runID":     r.cfg.Backfill.RunID,
		"projectID": r.cfg.Backfill.ProjectID,
		"table":     r.cfg.BackfillFilter.Table,
		"chunks":    len(days),
		"startDate": r.cfg.Backfill.PartDateRange.Start,
		"endDate":   r.cfg.Backfill.PartDateRange.End,
	}).Info("backfill: starting")

	// Spin up the progress bar; refresh in TTY mode every 500ms, in non-TTY
	// mode every 10s (one line at a time).
	r.progress = NewProgressBar(&r.stats, len(days))
	stop := make(chan struct{})
	tickInterval := 500 * time.Millisecond
	if !r.progress.isTTY {
		tickInterval = 10 * time.Second
	}
	progressDone := r.progress.StartTicker(tickInterval, stop)
	defer func() {
		close(stop)
		<-progressDone
	}()

	for _, day := range days {
		select {
		case <-ctx.Done():
			r.logger.Warn("backfill: context cancelled, exiting")
			return ctx.Err()
		default:
		}

		r.progress.SetCurrentChunk(day)

		if err := r.runDay(ctx, day); err != nil {
			r.stats.DaysFailed.Add(1)
			r.progress.MarkChunkFailed()
			if markErr := r.checkpoint.MarkFailed(ctx, day, err); markErr != nil {
				r.logger.WithError(markErr).Warn("backfill: mark chunk failed")
			}
			r.logger.WithError(err).WithField("chunk", day).Error("backfill: chunk failed; continuing")
			continue
		}

		r.stats.DaysCompleted.Add(1)
		r.progress.MarkChunkDone()
		r.logger.WithField("chunk", day).Info("backfill: chunk completed")
	}

	r.printSummary()
	return nil
}

// runDay performs one day's submit→poll→paginate→ingest cycle. It supports
// resume from any saved (taskId, pageId) state in the checkpoint.
func (r *Runner) runDay(ctx context.Context, day string) error {
	progress := r.checkpoint.Day(day)
	sql := r.buildDaySQL(day)

	// Acquire a valid taskId, either resuming an existing one or submitting fresh.
	if progress.TaskID == "" || progress.Status == DayPending || progress.Status == DayFailed {
		taskID, err := r.client.SubmitSQL(ctx, sql, r.cfg.Backfill.EffectivePageSize())
		if err != nil {
			r.stats.HTTPErrors.Add(1)
			return fmt.Errorf("submit: %w", err)
		}
		progress = DayProgress{
			Status: DayInProgress,
			TaskID: taskID,
			PageID: -1,
		}
		if err := r.checkpoint.SetDay(ctx, day, progress); err != nil {
			return err
		}
	}

	// Wait for the task to finish, with timeout.
	info, err := r.awaitFinished(ctx, progress.TaskID)
	if err != nil {
		if errors.Is(err, ErrTaskExpired) {
			r.logger.WithField("day", day).Warn("backfill: task expired; resubmitting")
			return r.resubmitDay(ctx, day, sql)
		}
		return err
	}

	progress.PageCount = info.ResultStat.PageCount
	if err := r.checkpoint.SetDay(ctx, day, progress); err != nil {
		return err
	}
	if r.progress != nil {
		r.progress.SetPageInfo(progress.PageID, progress.PageCount)
	}
	r.logger.WithFields(logrus.Fields{
		"chunk":     day,
		"taskId":    progress.TaskID,
		"pageCount": info.ResultStat.PageCount,
		"rowCount":  info.ResultStat.RowCount,
		"headers":   info.ResultStat.Headers,
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
				r.logger.WithField("day", day).Warn("backfill: task expired mid-paginate; resubmitting from page 0")
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
		if r.progress != nil {
			r.progress.SetPageInfo(pageID, progress.PageCount)
		}
	}

	return r.checkpoint.MarkCompleted(ctx, day, progress.Rows)
}

func (r *Runner) printSummary() {
	r.logger.WithFields(logrus.Fields{
		"days_completed":  r.stats.DaysCompleted.Load(),
		"days_failed":     r.stats.DaysFailed.Load(),
		"pages":           r.stats.Pages.Load(),
		"http_errors":     r.stats.HTTPErrors.Load(),
		"total_lines":     r.stats.TotalLines.Load(),
		"parsed_ok":       r.stats.ParsedOK.Load(),
		"parse_errors":    r.stats.ParseErrors.Load(),
		"identity_errors": r.stats.IdentityErrors.Load(),
		"user_writes":     r.stats.UserWrites.Load(),
		"event_writes":    r.stats.EventWrites.Load(),
		"dead_letters":    r.stats.DeadLetters.Load(),
		"write_errors":    r.stats.WriteErrors.Load(),
		"filtered":        r.stats.Filtered.Load(),
		"filter_errors":   r.stats.FilterErrors.Load(),
	}).Info("backfill: ========== summary ==========")
}
