// Package once implements a one-shot processing mode that reads all existing
// log files from the beginning, processes them to completion, and exits with
// a detailed summary of statistics including retries, errors, and throughput.
//
// Unlike the daemon mode which tails files incrementally and runs forever,
// the once mode is designed for batch migration, data recovery, or CI/CD
// pipeline scenarios where you want to process all existing data and get
// a clear pass/fail result.
package once

import (
	"bufio"
	"context"
	"fmt"
	"os"
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

// Stats holds detailed processing statistics for the once run.
type Stats struct {
	FilesDiscovered int64
	TotalLines      atomic.Int64
	ParsedOK        atomic.Int64
	ParseErrors     atomic.Int64
	IdentityErrors  atomic.Int64
	UserWrites      atomic.Int64
	EventWrites     atomic.Int64
	DeadLetters     atomic.Int64
	WriteErrors     atomic.Int64
	Filtered        atomic.Int64
	FilterErrors    atomic.Int64
	Retries         int64 // populated from store.WriteStats at the end
	StartTime       time.Time
	EndTime         time.Time
}

// Duration returns the total processing duration.
func (s *Stats) Duration() time.Duration {
	return s.EndTime.Sub(s.StartTime)
}

// HasErrors returns true if any errors occurred during processing.
func (s *Stats) HasErrors() bool {
	return s.ParseErrors.Load() > 0 ||
		s.IdentityErrors.Load() > 0 ||
		s.WriteErrors.Load() > 0
}

// statsCollector adapts Stats to the pipeline.StatsCollector interface.
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

// Runner is the one-shot processor.
type Runner struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.Store
	parser *talog.Parser
	filter *filter.Holder
	client *mongo.Client
	stats  Stats
}

// New connects to MongoDB and creates a ready-to-run Runner.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Runner, error) {
	flt, err := cfg.BuildFilter()
	if err != nil {
		return nil, fmt.Errorf("once: %w", err)
	}

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, fmt.Errorf("once: connect to mongo: %w", err)
	}

	dbName, err := config.MongoDBFromURI(cfg.MongoURI)
	if err != nil {
		return nil, fmt.Errorf("once: %w", err)
	}
	db := client.Database(dbName)
	st := store.New(db, cfg, logger)

	return &Runner{
		cfg:    cfg,
		logger: logger,
		store:  st,
		parser: talog.NewParser(),
		filter: filter.NewHolder(flt),
		client: client,
	}, nil
}

// Close disconnects the MongoDB client.
func (r *Runner) Close() error {
	return r.client.Disconnect(context.Background())
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (r *Runner) EnsureIndexes(ctx context.Context) error {
	return r.store.EnsureIndexes(ctx)
}

// Run executes the one-shot processing: discovers files, reads them from the
// beginning, processes all lines, and returns the final statistics.
// It returns an error only for fatal infrastructure issues (e.g. no log patterns).
// Individual line failures are captured in Stats, not as a returned error.
func (r *Runner) Run(ctx context.Context) (*Stats, error) {
	if len(r.cfg.LogPattern) == 0 {
		return nil, fmt.Errorf("once: ta.logPattern is required (at least one regex)")
	}

	r.stats.StartTime = time.Now()

	// Discover files using the same logic as the tailer (regex-based).
	files := tailer.DiscoverFiles(r.cfg.LogPattern, r.logger)
	r.stats.FilesDiscovered = int64(len(files))

	if len(files) == 0 {
		r.logger.Warn("once: no files matched ta.logPattern, nothing to process")
		r.stats.EndTime = time.Now()
		return &r.stats, nil
	}

	r.logger.WithField("files", len(files)).Info("once: discovered files to process")

	// Feed all file lines into a channel.
	lineCh := r.readAllFiles(ctx, files)

	// Process lines using the shared worker pipeline with stats collection.
	pipeline.RunWorkers(ctx, r.cfg, r.store, r.parser, r.filter, r.logger, lineCh, &statsCollector{s: &r.stats}, pipeline.WriteOptions{})

	r.stats.Retries = r.store.Stats().TotalRetries()
	r.stats.EndTime = time.Now()

	r.printSummary()

	return &r.stats, nil
}

// readAllFiles reads all discovered files from the beginning (not follow mode)
// and sends every non-empty line to the returned channel.
func (r *Runner) readAllFiles(ctx context.Context, files []string) <-chan string {
	out := make(chan string, 2000)

	go func() {
		defer close(out)
		for _, path := range files {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if err := r.readFile(ctx, path, out); err != nil {
				r.logger.WithError(err).WithField("path", path).Error("once: failed to read file")
			}
		}
	}()

	return out
}

// readFile opens a file and sends all non-empty lines to out.
func (r *Runner) readFile(ctx context.Context, path string, out chan<- string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	r.logger.WithField("path", path).Debug("once: reading file from beginning")

	scanner := bufio.NewScanner(f)
	// Support lines up to 10 MB (same as ingest mode).
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line := scanner.Text()
		if len(line) == 0 {
			continue
		}

		select {
		case out <- line:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return scanner.Err()
}

// printSummary logs a detailed summary of the processing run.
func (r *Runner) printSummary() {
	s := &r.stats
	duration := s.Duration()

	r.logger.Info("========== once mode: processing summary ==========")
	r.logger.WithFields(logrus.Fields{
		"files_discovered": s.FilesDiscovered,
		"total_lines":      s.TotalLines.Load(),
		"duration":         duration.Round(time.Millisecond),
	}).Info("overview")

	r.logger.WithFields(logrus.Fields{
		"parsed_ok":       s.ParsedOK.Load(),
		"parse_errors":    s.ParseErrors.Load(),
		"identity_errors": s.IdentityErrors.Load(),
	}).Info("parsing")

	r.logger.WithFields(logrus.Fields{
		"user_writes":  s.UserWrites.Load(),
		"event_writes": s.EventWrites.Load(),
		"dead_letters": s.DeadLetters.Load(),
	}).Info("writes")

	r.logger.WithFields(logrus.Fields{
		"filtered":      s.Filtered.Load(),
		"filter_errors": s.FilterErrors.Load(),
	}).Info("filter")

	r.logger.WithFields(logrus.Fields{
		"total_retries": s.Retries,
		"write_errors":  s.WriteErrors.Load(),
	}).Info("reliability")

	if totalLines := s.TotalLines.Load(); totalLines > 0 && duration > 0 {
		lps := float64(totalLines) / duration.Seconds()
		r.logger.WithField("lines_per_second", fmt.Sprintf("%.0f", lps)).Info("throughput")
	}

	if s.HasErrors() {
		r.logger.Warn("========== once mode: COMPLETED WITH ERRORS ==========")
	} else {
		r.logger.Info("========== once mode: COMPLETED SUCCESSFULLY ==========")
	}
}
