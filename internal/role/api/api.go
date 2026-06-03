// Package api is the embeddable ingestion engine — the "library" role. It
// connects to MongoDB once, then runs log lines through any of the three
// process strategies (single / batch / pipeline) over any source.Source.
//
// It is meant to be used directly as a library (import api, call New + Upload),
// and is embedded by the gateway role (HTTP face over an httpbody source) and
// the cli role (console face over a stdin source). All three therefore expose
// the same three upload functions.
//
// Usage:
//
//	cli, err := api.New(ctx, &dao.Config{Mongo: &mongo.Config{URI: uri}}, nil, nil)
//	if err != nil { ... }
//	defer cli.Close()
//	cli.EnsureIndexes(ctx)
//	res, _ := cli.Upload(ctx, process.ModeBatch, lines)
package api

import (
	"context"
	"fmt"

	"rocket-nano/tools/tango/internal/dao"
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

// Client is the connection-pool-backed ingestion engine. It is safe for
// concurrent use; the MongoDB driver manages the pool, and each upload run uses
// its own stats collector.
type Client struct {
	dao    *dao.Dao
	parser *parser.Parser
	cfg    *process.Config
}

// New connects to MongoDB and builds the engine. procCfg tunes the single/batch
// flush size and the pipeline worker pool (nil uses defaults); filterCfg is the
// optional reporting filter applied to every line (nil keeps everything). The
// caller must Close it.
func New(ctx context.Context, daoCfg *dao.Config, procCfg *process.Config, filterCfg *filter.Config) (*Client, error) {
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

	return &Client{dao: da, parser: p, cfg: procCfg}, nil
}

// Close disconnects from MongoDB and releases all resources.
func (c *Client) Close() error {
	return c.dao.Mongo.Close()
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (c *Client) EnsureIndexes(ctx context.Context) error {
	return c.dao.Store.EnsureIndexes(ctx)
}

// Run processes the source with the given mode, blocking until the source is
// drained or ctx is cancelled, and returns per-run statistics. Lines that fail
// to parse or resolve identity are routed to dead_letter (counted in the
// result) rather than returned as errors; a non-nil error indicates a bulk
// write failure or an unknown mode.
func (c *Client) Run(ctx context.Context, mode process.Mode, src source.Source) (Result, error) {
	stats := &process.Counters{}
	up, err := process.New(mode, c.cfg, c.dao, c.parser, stats, process.WriteOptions{})
	if err != nil {
		return Result{}, err
	}
	if err := up.Run(ctx, src); err != nil {
		return Result{}, err
	}
	s := stats.Snapshot()
	return Result{
		Lines:       s.TotalLines,
		UserWrites:  s.UserWrites,
		EventWrites: s.EventWrites,
		DeadLetters: s.DeadLetters,
		Filtered:    s.Filtered,
	}, nil
}

// Upload wraps lines as an httpbody source and runs them with the given mode.
// It is a convenience over Run for the common in-memory case.
func (c *Client) Upload(ctx context.Context, mode process.Mode, lines []string) (Result, error) {
	return c.Run(ctx, mode, httpbody.New(lines))
}
