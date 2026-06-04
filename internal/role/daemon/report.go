// Package daemon implements the long-running report service, orchestrating the
// tango pipeline: file tailing -> line parsing -> batch accumulation ->
// MongoDB bulk writes.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/source"
)

// statsReportInterval is how often the report service logs processing statistics.
const statsReportInterval = 60 * time.Second

// Service is the main runtime that connects all components together. It is built
// from the module configs the daemon needs (dao + parser + source + process),
// not from the top-level config package. It depends only on the source package,
// reaching the file-tailing config through srcCfg.Tailer rather than importing
// source/tailer directly.
type Service struct {
	dao     *dao.Dao
	parser  *parser.Parser
	srcCfg  *source.Config
	procCfg *process.Config
}

// New connects to MongoDB and creates a ready-to-run Service from the dao,
// parser, source, and process module configs. The caller must call Shutdown
// after Run returns to disconnect from MongoDB.
func New(ctx context.Context, daoCfg *dao.Config, parserCfg *parser.Config, srcCfg *source.Config, procCfg *process.Config) (*Service, error) {
	if srcCfg == nil || srcCfg.Tailer == nil {
		return nil, fmt.Errorf("report: source.tailer configuration is required")
	}
	if procCfg == nil {
		procCfg = &process.Config{}
	}
	procCfg.ApplyDefaults()

	p, err := parserCfg.Build()
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}

	da, err := dao.New(ctx, daoCfg)
	if err != nil {
		return nil, fmt.Errorf("report: %w", err)
	}

	return &Service{dao: da, parser: p, srcCfg: srcCfg, procCfg: procCfg}, nil
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
// and consistently hashes it to a fixed worker, so on the common path all
// operations for one user are handled in order by a single worker. This affinity
// is best-effort: under backpressure (the target worker's channel is full) the
// dispatcher spills the line to another worker to avoid head-of-line blocking,
// so strict cross-worker ordering is not guaranteed. Correctness does not rely
// on it — the write models use _ts conditional updates, so an older record can
// never overwrite a newer one regardless of which worker applies it.
func (d *Service) Run(ctx context.Context) error {
	tcfg := d.srcCfg.Tailer
	if len(tcfg.LogPattern) == 0 {
		return errors.New("report: source.tailer.logPattern is required (at least one regex)")
	}

	logging.WithFields(logging.Fields{
		"log_patterns":   tcfg.LogPattern,
		"workers":        d.procCfg.Pipeline.BatchWorkers,
		"batch_size":     d.procCfg.Pipeline.BatchSize,
		"flush_interval": d.procCfg.Pipeline.FlushInterval,
		"tail_mode":      tcfg.TailMode,
	}).Info("report: starting pipeline")

	// Build the tailer source via the source facade; the pipeline uploader runs it.
	t := source.NewTailer(tcfg)

	// Create stats collector for periodic reporting.
	stats := &process.Counters{}
	startTime := time.Now()

	// Launch periodic stats reporter.
	reportDone := make(chan struct{})
	go d.reportStats(ctx, stats, startTime, reportDone)

	// Drive the async pipeline strategy; it blocks until ctx is cancelled, then
	// the workers flush and exit.
	procCfg := *d.procCfg
	procCfg.Mode = string(process.ModePipeline)
	up, err := process.New(&procCfg, d.dao, d.parser, stats)
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
	defer logging.Recover("daemon stats reporter")

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
		"total_retries":      d.dao.Store.Stats().TotalRetries(),
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
