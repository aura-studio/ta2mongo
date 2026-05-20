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
}

func (s *daemonStats) OnLine()          { s.totalLines.Add(1) }
func (s *daemonStats) OnParseOK()       { s.parsedOK.Add(1) }
func (s *daemonStats) OnParseError()    { s.parseErrors.Add(1) }
func (s *daemonStats) OnIdentityError() { s.identityErrors.Add(1) }
func (s *daemonStats) OnUserWrite()     { s.userWrites.Add(1) }
func (s *daemonStats) OnEventWrite()    { s.eventWrites.Add(1) }
func (s *daemonStats) OnDeadLetter()    { s.deadLetters.Add(1) }
func (s *daemonStats) OnWriteError()    { s.writeErrors.Add(1) }

// snapshot returns the current counter values for reporting.
func (s *daemonStats) snapshot() (totalLines, parsedOK, parseErrors, identityErrors, userWrites, eventWrites, deadLetters, writeErrors int64) {
	return s.totalLines.Load(),
		s.parsedOK.Load(),
		s.parseErrors.Load(),
		s.identityErrors.Load(),
		s.userWrites.Load(),
		s.eventWrites.Load(),
		s.deadLetters.Load(),
		s.writeErrors.Load()
}

// Daemon is the main runtime that connects all components together.
type Daemon struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.Store
	parser *talog.Parser
	client *mongo.Client
}

// New connects to MongoDB and creates a ready-to-run Daemon.
// The caller must call Shutdown after Run returns to disconnect from MongoDB.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Daemon, error) {
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

	return &Daemon{cfg: cfg, logger: logger, store: st, parser: p, client: client}, nil
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
	pipeline.RunWorkers(ctx, d.cfg, d.store, d.parser, d.logger, lineCh, stats)

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

	// Track previous values to compute per-interval deltas.
	var prevTotal, prevParsedOK, prevParseErrors, prevIdentityErrors,
		prevUserWrites, prevEventWrites, prevDeadLetters, prevWriteErrors int64

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			total, parsedOK, parseErrors, identityErrors,
				userWrites, eventWrites, deadLetters, writeErrors := stats.snapshot()

			// Compute deltas since last report.
			deltaTotal := total - prevTotal
			deltaParsedOK := parsedOK - prevParsedOK
			deltaParseErrors := parseErrors - prevParseErrors
			deltaIdentityErrors := identityErrors - prevIdentityErrors
			deltaUserWrites := userWrites - prevUserWrites
			deltaEventWrites := eventWrites - prevEventWrites
			deltaDeadLetters := deadLetters - prevDeadLetters
			deltaWriteErrors := writeErrors - prevWriteErrors

			uptime := time.Since(startTime).Round(time.Second)

			d.logger.WithFields(logrus.Fields{
				"uptime":               uptime,
				"interval_lines":       deltaTotal,
				"interval_parsed_ok":   deltaParsedOK,
				"interval_parse_err":   deltaParseErrors,
				"interval_identity_err": deltaIdentityErrors,
				"interval_user_writes": deltaUserWrites,
				"interval_event_writes": deltaEventWrites,
				"interval_dead_letters": deltaDeadLetters,
				"interval_write_err":   deltaWriteErrors,
			}).Info("daemon: periodic stats (last 60s)")

			d.logger.WithFields(logrus.Fields{
				"total_lines":       total,
				"total_parsed_ok":   parsedOK,
				"total_parse_err":   parseErrors,
				"total_identity_err": identityErrors,
				"total_user_writes": userWrites,
				"total_event_writes": eventWrites,
				"total_dead_letters": deadLetters,
				"total_write_err":   writeErrors,
			}).Info("daemon: cumulative stats")

			// Update previous values for next delta calculation.
			prevTotal = total
			prevParsedOK = parsedOK
			prevParseErrors = parseErrors
			prevIdentityErrors = identityErrors
			prevUserWrites = userWrites
			prevEventWrites = eventWrites
			prevDeadLetters = deadLetters
			prevWriteErrors = writeErrors
		}
	}
}

// logFinalStats logs a final summary when the daemon is shutting down.
func (d *Daemon) logFinalStats(stats *daemonStats, startTime time.Time) {
	total, parsedOK, parseErrors, identityErrors,
		userWrites, eventWrites, deadLetters, writeErrors := stats.snapshot()

	duration := time.Since(startTime).Round(time.Second)

	d.logger.Info("daemon: ========== shutdown summary ==========")
	d.logger.WithFields(logrus.Fields{
		"total_lines":        total,
		"total_parsed_ok":    parsedOK,
		"total_parse_errors": parseErrors,
		"total_identity_err": identityErrors,
		"total_user_writes":  userWrites,
		"total_event_writes": eventWrites,
		"total_dead_letters": deadLetters,
		"total_write_errors": writeErrors,
		"uptime":             duration,
	}).Info("daemon: final stats")

	if total > 0 && duration.Seconds() > 0 {
		lps := float64(total) / duration.Seconds()
		d.logger.WithField("lines_per_second", fmt.Sprintf("%.1f", lps)).Info("daemon: average throughput")
	}

	if parseErrors > 0 || identityErrors > 0 || writeErrors > 0 {
		d.logger.Warn("daemon: ========== SHUTDOWN WITH ERRORS ==========")
	} else {
		d.logger.Info("daemon: ========== SHUTDOWN COMPLETE ==========")
	}
}
