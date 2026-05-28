// Package daemon orchestrates the tango pipeline:
// file tailing -> line parsing -> batch accumulation -> MongoDB bulk writes.
package daemon

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
	"rocket-nano/tools/tango/internal/filter"
	"rocket-nano/tools/tango/internal/pipeline"
	"rocket-nano/tools/tango/internal/store"
	"rocket-nano/tools/tango/internal/tailer"
	"rocket-nano/tools/tango/internal/talog"
)

// statsReportInterval is how often the daemon logs processing statistics.
const statsReportInterval = 60 * time.Second

// daemonStats tracks processing metrics for the daemon mode.
type daemonStats struct {
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

func (s *daemonStats) OnLine()          { s.totalLines.Add(1) }
func (s *daemonStats) OnParseOK()       { s.parsedOK.Add(1) }
func (s *daemonStats) OnParseError()    { s.parseErrors.Add(1) }
func (s *daemonStats) OnIdentityError() { s.identityErrors.Add(1) }
func (s *daemonStats) OnUserWrite()     { s.userWrites.Add(1) }
func (s *daemonStats) OnEventWrite()    { s.eventWrites.Add(1) }
func (s *daemonStats) OnDeadLetter()    { s.deadLetters.Add(1) }
func (s *daemonStats) OnWriteError()    { s.writeErrors.Add(1) }
func (s *daemonStats) OnFiltered()      { s.filtered.Add(1) }
func (s *daemonStats) OnFilterError()   { s.filterErrors.Add(1) }

// daemonSnapshot is a point-in-time copy of counter values used for reporting.
type daemonSnapshot struct {
	totalLines, parsedOK, parseErrors, identityErrors int64
	userWrites, eventWrites, deadLetters, writeErrors int64
	filtered, filterErrors                            int64
}

// snapshot returns the current counter values for reporting.
func (s *daemonStats) snapshot() daemonSnapshot {
	return daemonSnapshot{
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

// Daemon is the main runtime that connects all components together.
type Daemon struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.Store
	parser *talog.Parser
	filter *filter.Filter
	client *mongo.Client
}

// New connects to MongoDB and creates a ready-to-run Daemon.
// The caller must call Shutdown after Run returns to disconnect from MongoDB.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Daemon, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("daemon: %w", err)
	}

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, err
	}

	dbName, err := config.MongoDBFromURI(cfg.MongoURI)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("daemon: %w", err)
	}
	db := client.Database(dbName)
	st := store.New(db, cfg, logger)
	p := talog.NewParser()

	return &Daemon{cfg: cfg, logger: logger, store: st, parser: p, filter: flt, client: client}, nil
}

// Shutdown disconnects the MongoDB client. It must be called after Run returns
// to ensure all final flushes complete before the connection is closed.
func (d *Daemon) Shutdown() error {
	return d.client.Disconnect(context.Background())
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (d *Daemon) EnsureIndexes(ctx context.Context) error {
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
func (d *Daemon) Run(ctx context.Context) error {
	if len(d.cfg.LogPattern) == 0 {
		return errors.New("daemon: ta.logPattern is required (at least one regex)")
	}

	d.logger.WithFields(logrus.Fields{
		"log_patterns":    d.cfg.LogPattern,
		"workers":        d.cfg.BatchWorkers,
		"batch_size":     d.cfg.BatchSize,
		"flush_interval": d.cfg.FlushInterval,
		"tail_mode":      d.cfg.TailMode,
	}).Info("daemon: starting pipeline")

	// Start the tailer; it returns a channel of log lines.
	t := tailer.New(d.cfg.LogPattern, d.cfg.RescanInterval, d.cfg.TailMode, d.logger)
	lineCh := t.Run(ctx)

	// Create stats collector for periodic reporting.
	stats := &daemonStats{}
	startTime := time.Now()

	// Launch periodic stats reporter.
	reportDone := make(chan struct{})
	go d.reportStats(ctx, stats, startTime, reportDone)

	// Block until all workers finish.
	pipeline.RunWorkers(ctx, d.cfg, d.store, d.parser, d.filter, d.logger, lineCh, stats, pipeline.WriteOptions{})

	// Wait for the reporter goroutine to exit.
	<-reportDone

	// Log final stats summary.
	d.logFinalStats(stats, startTime)

	return nil
}

// reportStats periodically logs processing statistics every statsReportInterval.
func (d *Daemon) reportStats(ctx context.Context, stats *daemonStats, startTime time.Time, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(statsReportInterval)
	defer ticker.Stop()

	var prev daemonSnapshot

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
			}).Info("daemon: periodic stats (last 60s)")

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
			}).Info("daemon: cumulative stats")

			prev = cur
		}
	}
}

// logFinalStats logs a final summary when the daemon is shutting down.
func (d *Daemon) logFinalStats(stats *daemonStats, startTime time.Time) {
	cur := stats.snapshot()

	duration := time.Since(startTime).Round(time.Second)

	d.logger.Info("daemon: ========== shutdown summary ==========")
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
	}).Info("daemon: final stats")

	if cur.totalLines > 0 && duration.Seconds() > 0 {
		lps := float64(cur.totalLines) / duration.Seconds()
		d.logger.WithField("lines_per_second", fmt.Sprintf("%.1f", lps)).Info("daemon: average throughput")
	}

	if cur.parseErrors > 0 || cur.identityErrors > 0 || cur.writeErrors > 0 {
		d.logger.Warn("daemon: ========== SHUTDOWN WITH ERRORS ==========")
	} else {
		d.logger.Info("daemon: ========== SHUTDOWN COMPLETE ==========")
	}
}
