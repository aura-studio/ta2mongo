// Package daemon implements the long-running report service, orchestrating the
// tango pipeline: file tailing -> line parsing -> batch accumulation ->
// MongoDB bulk writes.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// statsReportInterval is how often the report service logs processing statistics.
const statsReportInterval = 60 * time.Second

// Service is the main runtime that connects all components together. It is built
// from the module configs the daemon needs (dao + parser + tailer source +
// process), not from the top-level config package.
type Service struct {
	dao    *dao.Dao
	parser *parser.Parser
	src    *tailer.Config
	proc   *process.Config
}

// New connects to MongoDB and creates a ready-to-run Service from the dao,
// parser, tailer-source, and process module configs. The caller must call
// Shutdown after Run returns to disconnect from MongoDB.
func New(ctx context.Context, daoCfg *dao.Config, parserCfg *parser.Config, srcCfg *tailer.Config, procCfg *process.Config) (*Service, error) {
	if srcCfg == nil {
		return nil, fmt.Errorf("report: source.tailer configuration is required")
	}

	p, err := parserCfg.Build()
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}

	da, err := dao.New(ctx, daoCfg)
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}

	return &Service{dao: da, parser: p, src: srcCfg, proc: procCfg}, nil
}

// Shutdown disconnects the MongoDB client. It must be called after Run returns
// to ensure all final flushes complete before the connection is closed.
func (d *Service) Shutdown() error {
	return d.dao.Mongo.Close()
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
	if len(d.src.LogPattern) == 0 {
		return errors.New("report: source.tailer.logPattern is required (at least one regex)")
	}

	logging.WithFields(logging.Fields{
		"log_patterns":   d.src.LogPattern,
		"workers":        d.proc.Pipeline.BatchWorkers,
		"batch_size":     d.proc.Pipeline.BatchSize,
		"flush_interval": d.proc.Pipeline.FlushInterval,
		"tail_mode":      d.src.TailMode,
	}).Info("report: starting pipeline")

	// Build the tailer source; the pipeline uploader runs it.
	t := tailer.New(d.src.LogPattern, d.src.RescanInterval, d.src.TailMode).WithTuning(d.src.PollInterval, d.src.MaxLineBytes)

	// Create stats collector for periodic reporting.
	stats := &process.Counters{}
	startTime := time.Now()

	// Launch periodic stats reporter.
	reportDone := make(chan struct{})
	go d.reportStats(ctx, stats, startTime, reportDone)

	// Drive the async pipeline strategy; it blocks until ctx is cancelled, then
	// the workers flush and exit.
	up, err := process.New(process.ModePipeline, d.proc, d.dao, d.parser, stats, process.WriteOptions{})
	if err != nil {
		return err
	}
	if err := up.Run(ctx, t); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

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

			logging.WithFields(logging.Fields{
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

			logging.WithFields(logging.Fields{
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

	logging.Info("report: ========== shutdown summary ==========")
	logging.WithFields(logging.Fields{
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
		logging.WithField("lines_per_second", fmt.Sprintf("%.1f", lps)).Info("report: average throughput")
	}

	if cur.ParseErrors > 0 || cur.IdentityErrors > 0 || cur.WriteErrors > 0 {
		logging.Warn("report: ========== SHUTDOWN WITH ERRORS ==========")
	} else {
		logging.Info("report: ========== SHUTDOWN COMPLETE ==========")
	}
}
