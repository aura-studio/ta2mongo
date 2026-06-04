// Package api is the embeddable ingestion engine — the "library" role. It
// connects to MongoDB once, then runs log lines through the process strategy
// configured by process.mode over any source.Source.
//
// It is meant to be used directly as a library (import api, call New + Upload),
// and is embedded by the gateway role (HTTP face over an httpbody source) and
// the cli role (console face over a stdin source). All three therefore use the
// same configured upload strategy.
//
// Usage:
//
//	eng, err := api.New(ctx, &dao.Config{Mongo: &mongo.Config{URI: uri}}, nil, nil)
//	if err != nil { ... }
//	defer eng.Close()
//	eng.EnsureIndexes(ctx)
//	res, _ := eng.Upload(ctx, lines)
package api

import (
	"context"
	"fmt"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/source"
	"rocket-nano/tools/tango/internal/source/httpbody"
)

// Result summarises a single upload run, derived from the run's stats.
type Result struct {
	Lines       int64 `json:"lines"`
	UserWrites  int64 `json:"userWrites"`
	EventWrites int64 `json:"eventWrites"`
	DeadLetters int64 `json:"deadLetters"`
	Filtered    int64 `json:"filtered"`
}

// Engine is the connection-pool-backed ingestion engine. It is safe for
// concurrent use; the MongoDB driver manages the pool, and each upload run uses
// its own stats collector.
type Engine struct {
	dao     *dao.Dao
	parser  *parser.Parser
	procCfg *process.Config
}

// New connects to MongoDB and builds the engine. procCfg selects the upload
// strategy and tunes the single/batch flush size and the pipeline worker pool
// (nil uses defaults); filterCfg is the optional reporting filter applied to
// every line (nil keeps everything). The caller must Close it.
func New(ctx context.Context, daoCfg *dao.Config, procCfg *process.Config, filterCfg *filter.Config) (*Engine, error) {
	if daoCfg == nil || daoCfg.Mongo == nil || daoCfg.Mongo.URI == "" {
		return nil, fmt.Errorf("api: MongoDB URI is required")
	}

	da, err := dao.New(ctx, daoCfg)
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}

	p, err := (&parser.Config{Filter: filterCfg}).Build()
	if err != nil {
		_ = da.Mongo.Close()
		return nil, fmt.Errorf("api: %w", err)
	}

	if procCfg == nil {
		procCfg = &process.Config{}
	}
	procCfg.ApplyDefaults()

	return &Engine{dao: da, parser: p, procCfg: procCfg}, nil
}

// Close disconnects from MongoDB and releases all resources.
func (c *Engine) Close() error {
	return c.dao.Mongo.Close()
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (c *Engine) EnsureIndexes(ctx context.Context) error {
	return c.dao.Store.EnsureIndexes(ctx)
}

// Run processes the source with c.procCfg.Mode, blocking until the source is
// drained or ctx is cancelled, and returns per-run statistics. Lines that fail
// to parse or resolve identity are routed to dead_letter (counted in the
// result) rather than returned as errors; a non-nil error indicates a bulk
// write failure or an unknown mode.
func (c *Engine) Run(ctx context.Context, src source.Source) (Result, error) {
	stats := &process.Counters{}
	mode, err := c.procCfg.ModeValue()
	if err != nil {
		return Result{}, err
	}
	up, err := process.New(c.procCfg, c.dao, c.parser, stats, process.WriteOptions{})
	if err != nil {
		return Result{}, err
	}
	if err := up.Run(ctx, src); err != nil {
		logging.WithField("mode", mode).WithError(err).Warn("api: upload failed")
		return Result{}, err
	}
	s := stats.Snapshot()
	res := Result{
		Lines:       s.TotalLines,
		UserWrites:  s.UserWrites,
		EventWrites: s.EventWrites,
		DeadLetters: s.DeadLetters,
		Filtered:    s.Filtered,
	}
	logging.WithFields(logging.Fields{
		"mode": mode, "lines": res.Lines, "user": res.UserWrites,
		"event": res.EventWrites, "dead": res.DeadLetters, "filtered": res.Filtered,
	}).Debug("api: upload complete")
	return res, nil
}

// Upload wraps lines as an httpbody source and runs them with c.procCfg.Mode.
// It is a convenience over Run for the common in-memory case.
func (c *Engine) Upload(ctx context.Context, lines []string) (Result, error) {
	return c.Run(ctx, httpbody.New(lines))
}
