// Package daemon implements the long-running report service (daemon mode), orchestrating the
// tango pipeline: file tailing -> line parsing -> batch accumulation ->
// MongoDB bulk writes.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/dao"
	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/log"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// statsReportInterval is how often the report service logs processing statistics.
const statsReportInterval = 60 * time.Second

// Service is the main runtime that connects all components together.
type Service struct {
	cfg    config.Config
	dao    *dao.Dao
	source *parser.Parser
	mongo  *daomongo.MongoResource
}

// New connects to MongoDB and creates a ready-to-run Service.
// The caller must call Shutdown after Run returns to disconnect from MongoDB.
func New(ctx context.Context, cfg config.Config) (*Service, error) {
	src, err := cfg.BuildParser()
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}

	res, err := daomongo.ConnectMongo(ctx, cfg.Dao.Mongo)
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}
	da := dao.New(res, cfg.Dao)

	return &Service{cfg: cfg, dao: da, source: src, mongo: res}, nil
}

// Shutdown disconnects the MongoDB client. It must be called after Run returns
// to ensure all final flushes complete before the connection is closed.
func (d *Service) Shutdown() error {
	return d.mongo.Close()
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (d *Service) EnsureIndexes(ctx context.Context) error {
	return d.dao.Store.EnsureIndexes(ctx)
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

	log.WithFields(log.Fields{
		"log_patterns":   d.cfg.Source.LogPattern,
		"workers":        d.cfg.Pipeline.BatchWorkers,
		"batch_size":     d.cfg.Pipeline.BatchSize,
		"flush_interval": d.cfg.Pipeline.FlushInterval,
		"tail_mode":      d.cfg.Source.TailMode,
	}).Info("report: starting pipeline")

	// Start the tailer; it returns a channel of log lines.
	t := tailer.New(d.cfg.Source.LogPattern, d.cfg.Source.RescanInterval, d.cfg.Source.TailMode).WithTuning(d.cfg.Source.PollInterval, d.cfg.Source.MaxLineBytes)
	lineCh := t.Run(ctx)

	// Create stats collector for periodic reporting.
	stats := &process.Counters{}
	startTime := time.Now()

	// Launch periodic stats reporter.
	reportDone := make(chan struct{})
	go d.reportStats(ctx, stats, startTime, reportDone)

	// Block until all workers finish.
	process.RunPipeline(ctx, d.cfg, d.dao, d.source, lineCh, stats, process.WriteOptions{})

	// Wait for the reporter goroutine to exit.
	<-reportDone

	// Log final stats summary.
	d.logFinalStats(stats, startTime)

	return nil
}

// reportStats periodically logs processing statistics every statsReportInterval.
func (d *Service) reportStats(ctx context.Context, stats *process.Counters, startTime time.Time, done chan<- struct{}) {
	defer close(done)

	ticker := time.NewTicker(statsReportInterval)
	defer ticker.Stop()

	var prev process.Snapshot

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := stats.Snapshot()

			uptime := time.Since(startTime).Round(time.Second)

			log.WithFields(log.Fields{
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

			log.WithFields(log.Fields{
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
func (d *Service) logFinalStats(stats *process.Counters, startTime time.Time) {
	cur := stats.Snapshot()

	duration := time.Since(startTime).Round(time.Second)

	log.Info("report: ========== shutdown summary ==========")
	log.WithFields(log.Fields{
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
		log.WithField("lines_per_second", fmt.Sprintf("%.1f", lps)).Info("report: average throughput")
	}

	if cur.ParseErrors > 0 || cur.IdentityErrors > 0 || cur.WriteErrors > 0 {
		log.Warn("report: ========== SHUTDOWN WITH ERRORS ==========")
	} else {
		log.Info("report: ========== SHUTDOWN COMPLETE ==========")
	}
}
