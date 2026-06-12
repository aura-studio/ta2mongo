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
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/source"
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
	dao        *dao.Dao
	parser     *parser.Parser
	procCfg    *process.Config
	cfgsyncCfg *cfgsync.Config
}

// New connects to MongoDB and builds the engine. procCfg selects the upload
// strategy and tunes the single/batch flush size and the pipeline worker pool
// (nil uses defaults); parserCfg carries the optional reporting filter applied
// to every line (nil, or an empty parser.filter.*, keeps everything);
// cfgsyncCfg carries the runtime config-sync settings used by StartCfgsync and
// PublishConfig (nil is defaulted to a disabled watcher with the default
// collection/document id). The caller must Close it.
func New(ctx context.Context, daoCfg *dao.Config, procCfg *process.Config, parserCfg *parser.Config, cfgsyncCfg *cfgsync.Config) (*Engine, error) {
	if daoCfg == nil || daoCfg.Mongo == nil || daoCfg.Mongo.URI == "" {
		return nil, fmt.Errorf("api: MongoDB URI is required")
	}

	da, err := dao.New(ctx, daoCfg)
	if err != nil {
		return nil, fmt.Errorf("api: %w", err)
	}

	p, err := parserCfg.Build()
	if err != nil {
		_ = da.Mongo.Close()
		return nil, fmt.Errorf("api: %w", err)
	}

	if procCfg == nil {
		procCfg = &process.Config{}
	}
	procCfg.ApplyDefaults()

	if cfgsyncCfg == nil {
		cfgsyncCfg = &cfgsync.Config{}
	}
	cfgsyncCfg.ApplyDefaults()

	return &Engine{dao: da, parser: p, procCfg: procCfg, cfgsyncCfg: cfgsyncCfg}, nil
}

// NewFromTree builds an Engine from the resolved top-level config tree, slicing
// the dao / process / parser / cfgsync branches it needs (each module owns its
// own FromTree decode + defaults + validation) and delegating to New. It is the
// wiring-layer counterpart to New: the gateway/role layers construct the engine
// from the whole config with one call, while library/SDK callers keep using New
// with typed module configs directly. The source is supplied per run (Upload),
// so no source branch is sliced here.
func NewFromTree(ctx context.Context, cfg cfgtree.Tree) (*Engine, error) {
	daoCfg, err := dao.FromTree(cfg)
	if err != nil {
		return nil, err
	}
	procCfg, err := process.FromTree(cfg)
	if err != nil {
		return nil, err
	}
	parserCfg, err := parser.FromTree(cfg)
	if err != nil {
		return nil, err
	}
	cfgsyncCfg, err := cfgsync.FromTree(cfg)
	if err != nil {
		return nil, err
	}
	return New(ctx, daoCfg, procCfg, parserCfg, cfgsyncCfg)
}

// Close disconnects from MongoDB and releases all resources.
func (c *Engine) Close() error {
	return c.dao.Mongo.Close()
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (c *Engine) EnsureIndexes(ctx context.Context) error {
	return c.dao.Store.EnsureIndexes(ctx)
}

// StartCfgsync launches the cfgsync Watcher in a goroutine when cfgsync is
// enabled, wiring the engine's own dao + parser so the live reporting filter is
// hot-swapped from the central config document (keeping last-good on a bad
// config). A disabled config is a no-op. The goroutine exits when ctx is
// cancelled; a fatal watcher error is logged rather than crashing the caller. It
// is used by the long-running gateway role (which embeds this engine and holds a
// live filter); the one-shot cli / library api callers do not start it.
func (c *Engine) StartCfgsync(ctx context.Context) {
	if !c.cfgsyncCfg.Enabled {
		return
	}
	reg := cfgsync.NewRegistry()
	cfgsync.RegisterFilter(reg, c.parser)
	w := cfgsync.New(c.dao, c.cfgsyncCfg, reg.Apply)
	go func() {
		defer logging.Recover("cfgsync watcher")
		if err := w.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logging.WithError(err).Error("cfgsync: watcher exited with error")
		}
	}()
}

// PublishConfig validates and publishes a runtime config document to the central
// collection, returning the new monotonic version. It is the api face of the
// shared cfgsync.Publish (same core as gateway POST /config and cli
// function=config) and is independent of the upload pipeline. doc carries the
// config content (e.g. {"filter": {"include": [...], "exclude": [...]}}); cfgsync
// owns _id and version. A document carrying a subtree off the allowlist, or a
// filter that does not compile, is rejected before any write.
func (c *Engine) PublishConfig(ctx context.Context, doc bson.M) (int64, error) {
	return cfgsync.Publish(ctx, c.dao, c.cfgsyncCfg, doc)
}

// AppendConfig is the merge flavour of PublishConfig: the published filter
// rules are unioned into the stored ones (an omitted include/exclude side keeps
// its stored value) under an optimistic version guard, so concurrent appends
// never lose each other's rules. Same validation gates as PublishConfig, run on
// the merged document.
func (c *Engine) AppendConfig(ctx context.Context, doc bson.M) (int64, error) {
	return cfgsync.PublishAppend(ctx, c.dao, c.cfgsyncCfg, doc)
}

// FetchConfig returns the current central config document (including its
// monotonic version), or (nil, nil) when nothing has been published yet. It is
// the query face operators use to inspect the live remote filter before
// appending to it (gateway GET /config and cli function=configget relay here).
func (c *Engine) FetchConfig(ctx context.Context) (bson.M, error) {
	return cfgsync.Fetch(ctx, c.dao, c.cfgsyncCfg)
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
	up, err := process.New(c.procCfg, c.dao, c.parser, stats)
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

// Upload wraps lines as an in-memory source and runs them with c.procCfg.Mode.
// It is a convenience over Run for the common in-memory case.
func (c *Engine) Upload(ctx context.Context, lines []string) (Result, error) {
	return c.Run(ctx, source.NewLines(lines))
}

// UploadFile imports the already-on-disk log files matching cfg's glob
// patterns once (the finite uploadfile source) and runs them with
// c.procCfg.Mode — the file-backed convenience over Run, backing cli
// function=uploadfile and the public client facade (the source construction
// sinks into the engine, so the client never touches the source domain).
// There is no checkpoint: re-running re-imports everything and converges
// through the idempotent write models. cfg must carry at least one pattern;
// patterns that match no files yield an empty Result.
func (c *Engine) UploadFile(ctx context.Context, cfg *UploadFileConfig) (Result, error) {
	if cfg == nil || len(cfg.LogPattern) == 0 {
		return Result{}, fmt.Errorf("api: uploadfile logPattern is required")
	}
	return c.Run(ctx, source.NewUploadFile(cfg))
}

// EJSON executes a Mongo Data API request against the engine's MongoDB
// connection and returns the result. It is the shared functional core behind the
// gateway POST /ejson endpoint and the cli ejson sub-mode, and is independent of
// the upload pipeline (process/parser config is unused). The request's database
// defaults to the one named in the connection URI when omitted. It relays
// through the dao facade (dao/ejson is fronted by dao).
func (c *Engine) EJSON(ctx context.Context, req *dao.EJSONRequest) (*dao.EJSONResponse, error) {
	return c.dao.EJSON(ctx, req)
}

// SQL parses and executes a SQL statement against the engine's MongoDB
// connection (SQL Data API) and returns the result. Like EJSON it relays through
// the dao facade (dao/sql is fronted by dao) and is independent of the upload
// pipeline.
func (c *Engine) SQL(ctx context.Context, query string) (*dao.SQLResult, error) {
	return c.dao.SQL(ctx, query)
}

// EJSONBytes is the bytes-in/bytes-out form of EJSON backing the public client
// facade: the Extended-JSON request shell is decoded here and the response
// re-encoded via its own MarshalEJSON. The client package must not touch dao
// types, so the dao-typed decode/encode sinks into the engine.
func (c *Engine) EJSONBytes(ctx context.Context, request []byte) ([]byte, error) {
	req, err := dao.DecodeEJSONRequest(request)
	if err != nil {
		return nil, err
	}
	resp, err := c.dao.EJSON(ctx, req)
	if err != nil {
		return nil, err
	}
	return resp.MarshalEJSON()
}

// SQLBytes is the bytes-out form of SQL backing the public client facade: the
// result is encoded here via its own MarshalEJSON. As with EJSONBytes, the
// client package must not touch dao types — the query side is already a plain
// string, so only the response encode sinks into the engine.
func (c *Engine) SQLBytes(ctx context.Context, query string) ([]byte, error) {
	res, err := c.dao.SQL(ctx, query)
	if err != nil {
		return nil, err
	}
	return res.MarshalEJSON()
}
