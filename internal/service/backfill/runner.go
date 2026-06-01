package backfill

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/filter"
	"rocket-nano/tools/tango/internal/core/store"
	"rocket-nano/tools/tango/internal/core/talog"
	"rocket-nano/tools/tango/internal/process/pipeline"
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
	filter     *filter.Holder
	client     *Client
	mongo      *mongo.Client
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
		_ = r.mongo.Disconnect(context.Background())
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
		_ = r.mongo.Disconnect(context.Background())
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

	mc, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Mongo.URI).SetConnectTimeout(cfg.Mongo.ConnectTimeout).SetServerSelectionTimeout(cfg.Mongo.ServerSelectionTimeout))
	if err != nil {
		return nil, nil, fmt.Errorf("backfill: connect mongo: %w", err)
	}
	dbName, err := config.MongoDBFromURI(cfg.Mongo.URI)
	if err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, nil, fmt.Errorf("backfill: %w", err)
	}
	db := mc.Database(dbName)
	st := store.New(db, cfg, logger)

	httpC, err := NewHTTPClient(cfg.Backfill.Proxy, 0)
	if err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, nil, fmt.Errorf("backfill: build http client: %w", err)
	}

	return &Runner{
		cfg:    cfg,
		logger: logger,
		store:  st,
		parser: talog.NewParser(),
		filter: filter.NewHolder(flt),
		client: NewClient(cfg.Backfill.APIBaseURL, cfg.Backfill.Token, httpC),
		mongo:  mc,
	}, db, nil
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

// userFlushBatch is the number of user-table snapshot upserts the runner
// accumulates before issuing a BulkWrite. Sized to cap per-flush latency
// while keeping the per-row overhead reasonable.
const userFlushBatch = 1000

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

// eventStreamBuffer is the per-worker channel buffer used when streaming rows
// from a result page into the pipeline. Sized to absorb a few flush cycles
// without blocking the HTTP read.
const eventStreamBuffer = 2048

func (r *Runner) fetchAndIngestEventPage(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	// The TA result-page endpoint returns one big NDJSON stream — pageCount is
	// always 1 regardless of pageSize, so a network blip mid-page would
	// otherwise lose the entire result. Stream rows directly into the worker
	// pipeline so each batch reaches Mongo before the next row is read, and
	// rely on #uuid dedup on retry.
	lineCh := make(chan string, eventStreamBuffer)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		pipeline.RunWorkers(ctx, r.cfg, r.store, r.parser, r.filter, r.logger,
			lineCh, &statsCollector{s: &r.stats},
			pipeline.WriteOptions{ForceSkipExisting: r.cfg.Backfill.ForceSkip()})
	}()

	rows := 0
	streamErr := r.client.StreamResultPage(ctx, taskID, pageID, r.cfg.Backfill.PageSize,
		func(row []interface{}) error {
			rows++
			line, err := EncodeRowAsJSONLine(headers, row)
			if err != nil {
				r.logger.WithError(err).Debug("backfill: encode row")
				return nil
			}
			select {
			case lineCh <- line:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})

	close(lineCh)
	<-workerDone
	return rows, streamErr
}

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

// buildDaySQL constructs the Presto SQL for one chunk of work.
//
// For the event table (`v_event_<pid>`), chunks are partition-dates and the
// SQL pins `"$part_date" = '<day>'`. For the user table (`v_user_<pid>`)
// there is no partition column, so the day argument is ignored and the
// SELECT pulls the whole table in one task. Optional eventTimeRange bounds
// (event table only) and the pushed-down filter WHERE are appended.
func (r *Runner) buildDaySQL(day string) string {
	var b strings.Builder
	b.WriteString(`SELECT * FROM `)
	if schema := r.cfg.Backfill.SchemaPrefix; schema != "" {
		fmt.Fprintf(&b, "%s.", schema)
	}
	switch r.cfg.BackfillFilter.Table {
	case config.BackfillTableUser:
		fmt.Fprintf(&b, "v_user_%d", r.cfg.Backfill.ProjectID)
	default:
		fmt.Fprintf(&b, "v_event_%d", r.cfg.Backfill.ProjectID)
	}

	predicates := []string{}
	if r.cfg.BackfillFilter.Table == config.BackfillTableEvent {
		predicates = append(predicates, fmt.Sprintf(`"$part_date" = '%s'`, day))
		if start := r.cfg.Backfill.EventTimeRange.Start; start != "" {
			predicates = append(predicates, fmt.Sprintf(`"#event_time" >= '%s'`, start))
		}
		if end := r.cfg.Backfill.EventTimeRange.End; end != "" {
			predicates = append(predicates, fmt.Sprintf(`"#event_time" <= '%s'`, end))
		}
	}
	if filterWhere, _ := r.cfg.BackfillWhere(); filterWhere != "" {
		predicates = append(predicates, filterWhere)
	}
	if len(predicates) > 0 {
		fmt.Fprintf(&b, " WHERE %s", strings.Join(predicates, " AND "))
	}
	if lim := r.cfg.Backfill.Limit; lim > 0 {
		fmt.Fprintf(&b, " LIMIT %d", lim)
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
