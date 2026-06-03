// Package report implements the report service runtime, orchestrating the
// tango pipeline: file tailing -> line parsing -> batch accumulation ->
// MongoDB bulk writes.
package report

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sirupsen/logrus"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/filter"
	coremongo "rocket-nano/tools/tango/internal/core/mongo"
	"rocket-nano/tools/tango/internal/core/store"
	"rocket-nano/tools/tango/internal/core/tailer"
	"rocket-nano/tools/tango/internal/core/talog"
	"rocket-nano/tools/tango/internal/process/ingestion"
	"rocket-nano/tools/tango/internal/process/pipeline"
)

// statsReportInterval is how often the report service logs processing statistics.
const statsReportInterval = 60 * time.Second

// Service is the main runtime that connects all components together.
type Service struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.Store
	parser *talog.Parser
	filter *filter.Holder
	mongo  *coremongo.MongoResource
}

// New connects to MongoDB and creates a ready-to-run Service.
// The caller must call Shutdown after Run returns to disconnect from MongoDB.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Service, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}

	res, err := coremongo.ConnectMongo(ctx, cfg.Mongo)
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}
	st := coremongo.NewStore(res.DB, cfg, logger)
	p := talog.NewParser()

	return &Service{cfg: cfg, logger: logger, store: st, parser: p, filter: filter.NewHolder(flt), mongo: res}, nil
}

// Shutdown disconnects the MongoDB client. It must be called after Run returns
// to ensure all final flushes complete before the connection is closed.
func (d *Service) Shutdown() error {
	return d.mongo.Close()
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
	stats := &ingestion.Counters{}
	startTime := time.Now()

	// Launch periodic stats reporter.
	reportDone := make(chan struct{})
	go d.reportStats(ctx, stats, startTime, reportDone)

	// Block until all workers finish.
	pipeline.RunWorkers(ctx, d.cfg, d.store, d.parser, d.filter, d.logger, lineCh, stats, ingestion.WriteOptions{})

	// Wait for the reporter goroutine to exit.
	<-reportDone

	// Log final stats summary.
	d.logFinalStats(stats, startTime)

	return nil
}

// reportStats periodically logs processing statistics every statsReportInterval.
func (d *Service) reportStats(ctx context.Context, stats *ingestion.Counters, startTime time.Time, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(statsReportInterval)
	defer ticker.Stop()

	var prev ingestion.Snapshot

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := stats.Snapshot()

			uptime := time.Since(startTime).Round(time.Second)

			d.logger.WithFields(logrus.Fields{
				"uptime":                uptime,
				"interval_lines":        cur.TotalLines - prev.TotalLines,
				"interval_parsed_ok":    cur.ParsedOK - prev.ParsedOK,
				"interval_parse_err":    cur.ParseErrors - prev.ParseErrors,
				"interval_identity_err": cur.IdentityErrors - prev.IdentityErrors,
				"interval_user_writes":  cur.UserWrites - prev.UserWrites,
				"interval_event_writes": cur.EventWrites - prev.EventWrites,
				"interval_dead_letters": cur.DeadLetters - prev.DeadLetters,
				"interval_write_err":    cur.WriteErrors - prev.WriteErrors,
				"interval_filtered":     cur.Filtered - prev.Filtered,
				"interval_filter_err":   cur.FilterErrors - prev.FilterErrors,
			}).Info("report: periodic stats (last 60s)")

			d.logger.WithFields(logrus.Fields{
				"total_lines":        cur.TotalLines,
				"total_parsed_ok":    cur.ParsedOK,
				"total_parse_err":    cur.ParseErrors,
				"total_identity_err": cur.IdentityErrors,
				"total_user_writes":  cur.UserWrites,
				"total_event_writes": cur.EventWrites,
				"total_dead_letters": cur.DeadLetters,
				"total_write_err":    cur.WriteErrors,
				"total_filtered":     cur.Filtered,
				"total_filter_err":   cur.FilterErrors,
			}).Info("report: cumulative stats")

			prev = cur
		}
	}
}

// logFinalStats logs a final summary when the daemon is shutting down.
func (d *Service) logFinalStats(stats *ingestion.Counters, startTime time.Time) {
	cur := stats.Snapshot()

	duration := time.Since(startTime).Round(time.Second)

	d.logger.Info("report: ========== shutdown summary ==========")
	d.logger.WithFields(logrus.Fields{
		"total_lines":        cur.TotalLines,
		"total_parsed_ok":    cur.ParsedOK,
		"total_parse_errors": cur.ParseErrors,
		"total_identity_err": cur.IdentityErrors,
		"total_user_writes":  cur.UserWrites,
		"total_event_writes": cur.EventWrites,
		"total_dead_letters": cur.DeadLetters,
		"total_write_errors": cur.WriteErrors,
		"total_filtered":     cur.Filtered,
		"total_filter_err":   cur.FilterErrors,
		"uptime":             duration,
	}).Info("report: final stats")

	if cur.TotalLines > 0 && duration.Seconds() > 0 {
		lps := float64(cur.TotalLines) / duration.Seconds()
		d.logger.WithField("lines_per_second", fmt.Sprintf("%.1f", lps)).Info("report: average throughput")
	}

	if cur.ParseErrors > 0 || cur.IdentityErrors > 0 || cur.WriteErrors > 0 {
		d.logger.Warn("report: ========== SHUTDOWN WITH ERRORS ==========")
	} else {
		d.logger.Info("report: ========== SHUTDOWN COMPLETE ==========")
	}
}
