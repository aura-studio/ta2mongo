// Package report implements the report service runtime, orchestrating the
// tango pipeline: file tailing -> line parsing -> batch accumulation ->
// MongoDB bulk writes, with optional remote-config filter hot-reload.
package report

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/filter"
	"rocket-nano/tools/tango/internal/core/remoteconfig"
	"rocket-nano/tools/tango/internal/core/store"
	"rocket-nano/tools/tango/internal/core/tailer"
	"rocket-nano/tools/tango/internal/core/talog"
	"rocket-nano/tools/tango/internal/process/pipeline"
)

// statsReportInterval is how often the daemon logs processing statistics.
const statsReportInterval = 60 * time.Second

// runStats tracks processing metrics for the daemon mode.
type runStats struct {
	totalLines     atomic.Int64
	parsedOK       atomic.Int64
	parseErrors    atomic.Int64
	identityErrors atomic.Int64
	userWrites     atomic.Int64
	eventWrites    atomic.Int64
	deadLetters    atomic.Int64
	writeErrors    atomic.Int64
	filtered       atomic.Int64
	filterErrors   atomic.Int64
}

func (s *runStats) OnLine()          { s.totalLines.Add(1) }
func (s *runStats) OnParseOK()       { s.parsedOK.Add(1) }
func (s *runStats) OnParseError()    { s.parseErrors.Add(1) }
func (s *runStats) OnIdentityError() { s.identityErrors.Add(1) }
func (s *runStats) OnUserWrite()     { s.userWrites.Add(1) }
func (s *runStats) OnEventWrite()    { s.eventWrites.Add(1) }
func (s *runStats) OnDeadLetter()    { s.deadLetters.Add(1) }
func (s *runStats) OnWriteError()    { s.writeErrors.Add(1) }
func (s *runStats) OnFiltered()      { s.filtered.Add(1) }
func (s *runStats) OnFilterError()   { s.filterErrors.Add(1) }

// runSnapshot is a point-in-time copy of counter values used for reporting.
type runSnapshot struct {
	totalLines, parsedOK, parseErrors, identityErrors int64
	userWrites, eventWrites, deadLetters, writeErrors int64
	filtered, filterErrors                            int64
}

// snapshot returns the current counter values for reporting.
func (s *runStats) snapshot() runSnapshot {
	return runSnapshot{
		totalLines:     s.totalLines.Load(),
		parsedOK:       s.parsedOK.Load(),
		parseErrors:    s.parseErrors.Load(),
		identityErrors: s.identityErrors.Load(),
		userWrites:     s.userWrites.Load(),
		eventWrites:    s.eventWrites.Load(),
		deadLetters:    s.deadLetters.Load(),
		writeErrors:    s.writeErrors.Load(),
		filtered:       s.filtered.Load(),
		filterErrors:   s.filterErrors.Load(),
	}
}

// Service is the main runtime that connects all components together.
type Service struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.Store
	parser *talog.Parser
	filter *filter.Holder
	client *mongo.Client
}

// New connects to MongoDB and creates a ready-to-run Service.
// The caller must call Shutdown after Run returns to disconnect from MongoDB.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Service, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Mongo.URI).SetConnectTimeout(cfg.Mongo.ConnectTimeout).SetServerSelectionTimeout(cfg.Mongo.ServerSelectionTimeout))
	if err != nil {
		return nil, err
	}

	dbName, err := config.MongoDBFromURI(cfg.Mongo.URI)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("report: %w", err)
	}
	db := client.Database(dbName)
	st := store.New(db, cfg, logger)
	p := talog.NewParser()

	return &Service{cfg: cfg, logger: logger, store: st, parser: p, filter: filter.NewHolder(flt), client: client}, nil
}

// Filter exposes the daemon's filter holder so the remote-config sync loop can
// hot-swap the active filter at runtime.
func (d *Service) Filter() *filter.Holder { return d.filter }

// MongoClient exposes the underlying client so callers can reuse the
// connection (e.g. the remote-config fetcher) without opening a second one.
func (d *Service) MongoClient() *mongo.Client { return d.client }

// Shutdown disconnects the MongoDB client. It must be called after Run returns
// to ensure all final flushes complete before the connection is closed.
func (d *Service) Shutdown() error {
	return d.client.Disconnect(context.Background())
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (d *Service) EnsureIndexes(ctx context.Context) error {
	return d.store.EnsureIndexes(ctx)
}

// Run starts the daemon pipeline and blocks until ctx is cancelled.
//
// Flow: tailer -> lineCh -> dispatcher (routes by user affinity) -> workerCh[i] -> worker_i -> MongoDB
//
// The dispatcher extracts #account_id (preferred) or #distinct_id from each line
// and consistently hashes it to a fixed worker. This guarantees that all operations
// for the same user are processed sequentially by a single worker, preventing
// out-of-order overwrites across workers.
func (d *Service) Run(ctx context.Context) error {
	if len(d.cfg.Source.LogPattern) == 0 {
		return errors.New("report: ta.logPattern is required (at least one regex)")
	}

	d.logger.WithFields(logrus.Fields{
		"log_patterns":   d.cfg.Source.LogPattern,
		"workers":        d.cfg.Pipeline.BatchWorkers,
		"batch_size":     d.cfg.Pipeline.BatchSize,
		"flush_interval": d.cfg.Pipeline.FlushInterval,
		"tail_mode":      d.cfg.Source.TailMode,
	}).Info("report: starting pipeline")

	// Start the tailer; it returns a channel of log lines.
	t := tailer.New(d.cfg.Source.LogPattern, d.cfg.Source.RescanInterval, d.cfg.Source.TailMode, d.logger).WithTuning(d.cfg.Source.PollInterval, d.cfg.Source.MaxLineBytes)
	lineCh := t.Run(ctx)

	// Create stats collector for periodic reporting.
	stats := &runStats{}
	startTime := time.Now()

	// Launch periodic stats reporter.
	reportDone := make(chan struct{})
	go d.reportStats(ctx, stats, startTime, reportDone)

	// Launch the remote-config hot-reload loop (no-op unless enabled). It
	// periodically re-fetches the override document and swaps the active
	// filter in place; other fields only take effect on the next restart.
	if d.cfg.RemoteConfig.Enabled {
		go d.syncRemoteConfig(ctx)
	}

	// Block until all workers finish.
	pipeline.RunWorkers(ctx, d.cfg, d.store, d.parser, d.filter, d.logger, lineCh, stats, pipeline.WriteOptions{})

	// Wait for the reporter goroutine to exit.
	<-reportDone

	// Log final stats summary.
	d.logFinalStats(stats, startTime)

	return nil
}

// syncRemoteConfig periodically re-fetches the remote override document and
// hot-swaps the active filter when the include/exclude lists change. Non-filter
// fields are reported but only take effect on the next restart (matching the
// agreed "filter hot, rest on restart" policy). It returns when ctx is done.
func (d *Service) syncRemoteConfig(ctx context.Context) {
	dbName, err := config.MongoDBFromURI(d.cfg.Mongo.URI)
	if err != nil {
		d.logger.WithError(err).Warn("report: remote-config sync disabled (bad mongoURI)")
		return
	}
	coll := d.client.Database(dbName).Collection(d.cfg.RemoteConfig.Collection)

	ticker := time.NewTicker(d.cfg.RemoteConfig.SyncInterval)
	defer ticker.Stop()

	d.logger.WithFields(logrus.Fields{
		"collection":    d.cfg.RemoteConfig.Collection,
		"documentID":    d.cfg.RemoteConfig.DocumentID,
		"sync_interval": d.cfg.RemoteConfig.SyncInterval,
	}).Info("report: remote-config sync loop started")

	// current is the most recently applied config; start from the boot config.
	current := d.cfg

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			doc, err := remoteconfig.Fetch(ctx, coll, d.cfg.RemoteConfig.DocumentID)
			if err != nil {
				d.logger.WithError(err).Warn("report: remote-config fetch failed; keeping current")
				continue
			}
			if doc == nil {
				continue
			}
			merged, err := remoteconfig.Merge(current, doc)
			if err != nil {
				d.logger.WithError(err).Warn("report: remote-config merge failed; keeping current")
				continue
			}
			if err := merged.Validate(); err != nil {
				d.logger.WithError(err).Warn("report: remote-config invalid; keeping current")
				continue
			}

			if remoteconfig.FilterChanged(current, merged) {
				newFilter, ferr := merged.BuildFilter()
				if ferr != nil {
					d.logger.WithError(ferr).Warn("report: remote filter failed to compile; keeping current")
					continue
				}
				d.filter.Store(newFilter)
				d.logger.WithFields(logrus.Fields{
					"filter_include": merged.Filter.Include,
					"filter_exclude": merged.Filter.Exclude,
				}).Info("report: hot-reloaded filter from remote config")
			}
			current = merged
		}
	}
}

// reportStats periodically logs processing statistics every statsReportInterval.
func (d *Service) reportStats(ctx context.Context, stats *runStats, startTime time.Time, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(statsReportInterval)
	defer ticker.Stop()

	var prev runSnapshot

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := stats.snapshot()

			uptime := time.Since(startTime).Round(time.Second)

			d.logger.WithFields(logrus.Fields{
				"uptime":                uptime,
				"interval_lines":        cur.totalLines - prev.totalLines,
				"interval_parsed_ok":    cur.parsedOK - prev.parsedOK,
				"interval_parse_err":    cur.parseErrors - prev.parseErrors,
				"interval_identity_err": cur.identityErrors - prev.identityErrors,
				"interval_user_writes":  cur.userWrites - prev.userWrites,
				"interval_event_writes": cur.eventWrites - prev.eventWrites,
				"interval_dead_letters": cur.deadLetters - prev.deadLetters,
				"interval_write_err":    cur.writeErrors - prev.writeErrors,
				"interval_filtered":     cur.filtered - prev.filtered,
				"interval_filter_err":   cur.filterErrors - prev.filterErrors,
			}).Info("report: periodic stats (last 60s)")

			d.logger.WithFields(logrus.Fields{
				"total_lines":        cur.totalLines,
				"total_parsed_ok":    cur.parsedOK,
				"total_parse_err":    cur.parseErrors,
				"total_identity_err": cur.identityErrors,
				"total_user_writes":  cur.userWrites,
				"total_event_writes": cur.eventWrites,
				"total_dead_letters": cur.deadLetters,
				"total_write_err":    cur.writeErrors,
				"total_filtered":     cur.filtered,
				"total_filter_err":   cur.filterErrors,
			}).Info("report: cumulative stats")

			prev = cur
		}
	}
}

// logFinalStats logs a final summary when the daemon is shutting down.
func (d *Service) logFinalStats(stats *runStats, startTime time.Time) {
	cur := stats.snapshot()

	duration := time.Since(startTime).Round(time.Second)

	d.logger.Info("report: ========== shutdown summary ==========")
	d.logger.WithFields(logrus.Fields{
		"total_lines":        cur.totalLines,
		"total_parsed_ok":    cur.parsedOK,
		"total_parse_errors": cur.parseErrors,
		"total_identity_err": cur.identityErrors,
		"total_user_writes":  cur.userWrites,
		"total_event_writes": cur.eventWrites,
		"total_dead_letters": cur.deadLetters,
		"total_write_errors": cur.writeErrors,
		"total_filtered":     cur.filtered,
		"total_filter_err":   cur.filterErrors,
		"uptime":             duration,
	}).Info("report: final stats")

	if cur.totalLines > 0 && duration.Seconds() > 0 {
		lps := float64(cur.totalLines) / duration.Seconds()
		d.logger.WithField("lines_per_second", fmt.Sprintf("%.1f", lps)).Info("report: average throughput")
	}

	if cur.parseErrors > 0 || cur.identityErrors > 0 || cur.writeErrors > 0 {
		d.logger.Warn("report: ========== SHUTDOWN WITH ERRORS ==========")
	} else {
		d.logger.Info("report: ========== SHUTDOWN COMPLETE ==========")
	}
}
