package backfill

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/filter"
	"rocket-nano/tools/tango/internal/pipeline"
	"rocket-nano/tools/tango/internal/store"
	"rocket-nano/tools/tango/internal/talog"
)

// Stats records counters for the backfill run. Mirrors pipeline.StatsCollector
// behaviour and adds backfill-specific counters (HTTP errors, pages fetched).
type Stats struct {
	TotalLines     atomic.Int64
	ParsedOK       atomic.Int64
	ParseErrors    atomic.Int64
	IdentityErrors atomic.Int64
	UserWrites     atomic.Int64
	EventWrites    atomic.Int64
	DeadLetters    atomic.Int64
	WriteErrors    atomic.Int64
	Filtered       atomic.Int64
	FilterErrors   atomic.Int64
	Pages          atomic.Int64
	HTTPErrors     atomic.Int64
	DaysCompleted  atomic.Int64
	DaysFailed     atomic.Int64
}

// statsCollector adapts Stats to pipeline.StatsCollector.
type statsCollector struct{ s *Stats }

func (c *statsCollector) OnLine()          { c.s.TotalLines.Add(1) }
func (c *statsCollector) OnParseOK()       { c.s.ParsedOK.Add(1) }
func (c *statsCollector) OnParseError()    { c.s.ParseErrors.Add(1) }
func (c *statsCollector) OnIdentityError() { c.s.IdentityErrors.Add(1) }
func (c *statsCollector) OnUserWrite()     { c.s.UserWrites.Add(1) }
func (c *statsCollector) OnEventWrite()    { c.s.EventWrites.Add(1) }
func (c *statsCollector) OnDeadLetter()    { c.s.DeadLetters.Add(1) }
func (c *statsCollector) OnWriteError()    { c.s.WriteErrors.Add(1) }
func (c *statsCollector) OnFiltered()      { c.s.Filtered.Add(1) }
func (c *statsCollector) OnFilterError()   { c.s.FilterErrors.Add(1) }

// Runner orchestrates one backfill execution.
type Runner struct {
	cfg        config.Config
	logger     *logrus.Logger
	store      *store.Store
	parser     *talog.Parser
	filter     *filter.Filter
	client     *Client
	mongo      *mongo.Client
	checkpoint *Checkpoint
	stats      Stats
}

// New connects to MongoDB and constructs a ready-to-run Runner. The caller
// must call Shutdown after Run returns.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Runner, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("backfill: %w", err)
	}

	mc, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, fmt.Errorf("backfill: connect mongo: %w", err)
	}
	dbName, err := config.MongoDBFromURI(cfg.MongoURI)
	if err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("backfill: %w", err)
	}
	db := mc.Database(dbName)
	st := store.New(db, cfg, logger)

	filterWhere, err := filter.CompileToSQL(cfg.FilterInclude, cfg.FilterExclude)
	if err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("backfill: %w", err)
	}

	sig := SQLSignature(cfg.Backfill.Table, cfg.Backfill.ProjectID, filterWhere,
		cfg.Backfill.EventTimeRange.Start, cfg.Backfill.EventTimeRange.End)

	cp, err := NewCheckpoint(ctx, db.Collection(cfg.Backfill.ProgressCollection),
		cfg.Backfill.RunID, cfg.Backfill.APIBaseURL, cfg.Backfill.ProjectID,
		cfg.Backfill.Table, cfg.Backfill.PartDateRange.Start, cfg.Backfill.PartDateRange.End, sig)
	if err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("backfill: %w", err)
	}

	return &Runner{
		cfg:        cfg,
		logger:     logger,
		store:      st,
		parser:     talog.NewParser(),
		filter:     flt,
		client:     NewClient(cfg.Backfill.APIBaseURL, cfg.Backfill.Token, nil),
		mongo:      mc,
		checkpoint: cp,
	}, nil
}

// Shutdown disconnects from MongoDB.
func (r *Runner) Shutdown() error { return r.mongo.Disconnect(context.Background()) }

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
		r.logger.Info("backfill: no pending days; nothing to do")
		return nil
	}

	r.logger.WithFields(logrus.Fields{
		"runID":     r.cfg.Backfill.RunID,
		"projectID": r.cfg.Backfill.ProjectID,
		"table":     r.cfg.Backfill.Table,
		"days":      len(days),
		"startDate": r.cfg.Backfill.PartDateRange.Start,
		"endDate":   r.cfg.Backfill.PartDateRange.End,
	}).Info("backfill: starting")

	for _, day := range days {
		select {
		case <-ctx.Done():
			r.logger.Warn("backfill: context cancelled, exiting")
			return ctx.Err()
		default:
		}

		if err := r.runDay(ctx, day); err != nil {
			r.stats.DaysFailed.Add(1)
			if markErr := r.checkpoint.MarkFailed(ctx, day, err); markErr != nil {
				r.logger.WithError(markErr).Warn("backfill: mark day failed")
			}
			r.logger.WithError(err).WithField("day", day).Error("backfill: day failed; continuing with next day")
			continue
		}

		r.stats.DaysCompleted.Add(1)
		r.logger.WithField("day", day).Info("backfill: day completed")
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
		taskID, err := r.client.SubmitSQL(ctx, sql)
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

	// Paginate from the next unprocessed page.
	startPage := progress.PageID + 1
	for pageID := startPage; pageID < progress.PageCount; pageID++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		page, err := r.client.ResultPage(ctx, progress.TaskID, pageID, r.cfg.Backfill.PageSize)
		if err != nil {
			if errors.Is(err, ErrTaskExpired) {
				r.logger.WithField("day", day).Warn("backfill: task expired mid-paginate; resubmitting from page 0")
				return r.resubmitDay(ctx, day, sql)
			}
			r.stats.HTTPErrors.Add(1)
			return fmt.Errorf("page %d: %w", pageID, err)
		}

		rows, ferr := r.ingestPage(ctx, page)
		if ferr != nil {
			return fmt.Errorf("page %d ingest: %w", pageID, ferr)
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

// resubmitDay clears the in-progress task state and starts the day over. The
// #uuid unique index keeps already-written rows safely deduped.
func (r *Runner) resubmitDay(ctx context.Context, day, sql string) error {
	taskID, err := r.client.SubmitSQL(ctx, sql)
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

// ingestPage runs one page through the pipeline. It spins up a fresh workers
// goroutine bundle per page so that checkpointing can advance only after the
// page has been fully written. This trades a small amount of per-page
// startup overhead for clean resume semantics.
func (r *Runner) ingestPage(ctx context.Context, page *ResultPageResult) (int, error) {
	lineCh := make(chan string, r.cfg.Backfill.PageSize)

	go func() {
		defer close(lineCh)
		for _, row := range page.Rows {
			line, err := EncodeRowAsJSONLine(page.Headers, row)
			if err != nil {
				r.logger.WithError(err).Debug("backfill: encode row")
				continue
			}
			select {
			case lineCh <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	pipeline.RunWorkers(ctx, r.cfg, r.store, r.parser, r.filter, r.logger,
		lineCh, &statsCollector{s: &r.stats},
		pipeline.WriteOptions{ForceSkipExisting: r.cfg.Backfill.ForceSkip()})

	return len(page.Rows), nil
}

// buildDaySQL constructs the per-day Presto SQL: SELECT * with $part_date
// pinned, optional event-time bounds, and the pushed-down filter WHERE.
func (r *Runner) buildDaySQL(day string) string {
	var b strings.Builder
	b.WriteString(`SELECT * FROM `)
	switch r.cfg.Backfill.Table {
	case config.BackfillTableUser:
		fmt.Fprintf(&b, "v_user_%d", r.cfg.Backfill.ProjectID)
	default:
		fmt.Fprintf(&b, "v_event_%d", r.cfg.Backfill.ProjectID)
	}
	fmt.Fprintf(&b, ` WHERE "$part_date" = '%s'`, day)

	if start := r.cfg.Backfill.EventTimeRange.Start; start != "" {
		fmt.Fprintf(&b, ` AND "#event_time" >= '%s'`, start)
	}
	if end := r.cfg.Backfill.EventTimeRange.End; end != "" {
		fmt.Fprintf(&b, ` AND "#event_time" <= '%s'`, end)
	}

	if filterWhere, _ := filter.CompileToSQL(r.cfg.FilterInclude, r.cfg.FilterExclude); filterWhere != "" {
		fmt.Fprintf(&b, ` AND %s`, filterWhere)
	}
	return b.String()
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
