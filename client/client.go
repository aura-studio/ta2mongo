// Package client is the public, redis-go-style client for tango's log-ingestion
// engine. Construct a Client with New plus functional With* options, then feed
// it ThinkingData log lines via Upload.
//
// It is a thin, stable facade over the internal ingestion engine
// (internal/role/api): callers depend only on this interface and never on
// tango's internal packages. The same engine backs the daemon/gateway/cli roles,
// so an Upload here uses the identical parse → filter → identity-resolve → write
// path.
//
// Usage:
//
//	c, err := client.New(
//		client.WithDaoMongoURI("mongodb://localhost:27017/tango"),
//		client.WithProcessMode("pipeline"),
//	)
//	if err != nil {
//		// ...
//	}
//	defer c.Close()
//	res, err := c.Upload(ctx, line1, line2)
package client

import (
	"context"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role/api"
)

// Result summarises a single Upload run.
type Result struct {
	Lines       int64 `json:"lines"`
	UserWrites  int64 `json:"userWrites"`
	EventWrites int64 `json:"eventWrites"`
	DeadLetters int64 `json:"deadLetters"`
	Filtered    int64 `json:"filtered"`
}

// Client is tango's ingestion client. It is safe for concurrent use; the
// underlying MongoDB driver manages the connection pool. Always Close it.
type Client interface {
	// Upload ingests the given TA log lines with the configured strategy and
	// returns per-run statistics. Lines that fail to parse or resolve identity
	// are routed to dead_letter (counted in Result) rather than erroring; a
	// non-nil error indicates a bulk-write failure or an unknown mode.
	Upload(ctx context.Context, lines ...string) (Result, error)
	// EnsureIndexes creates all required MongoDB indexes (idempotent).
	EnsureIndexes(ctx context.Context) error
	// Close disconnects from MongoDB and releases all resources.
	Close() error
}

type client struct {
	engine *api.Engine
}

// New connects to MongoDB and builds a Client from the given options.
// WithMongoURI is required; without it New returns an error. The caller must
// Close the returned Client.
func New(opts ...Option) (Client, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	daoCfg := &dao.Config{
		Mongo: &mongo.Config{
			URI:                    o.mongoURI,
			ConnectTimeout:         o.connectTimeout,
			ServerSelectionTimeout: o.serverSelectionTimeout,
		},
		Store: &store.Config{MaxElapsedTime: o.maxElapsedTime},
	}

	procCfg := &process.Config{
		Mode:      o.mode,
		BatchSize: o.batchSize,
		Pipeline:  o.pipeline,
	}

	var filterCfg *filter.Config
	if len(o.include) > 0 || len(o.exclude) > 0 {
		filterCfg = &filter.Config{Include: o.include, Exclude: o.exclude}
	}

	engine, err := api.New(o.ctx, daoCfg, procCfg, filterCfg)
	if err != nil {
		return nil, err
	}
	return &client{engine: engine}, nil
}

func (c *client) Upload(ctx context.Context, lines ...string) (Result, error) {
	res, err := c.engine.Upload(ctx, lines)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Lines:       res.Lines,
		UserWrites:  res.UserWrites,
		EventWrites: res.EventWrites,
		DeadLetters: res.DeadLetters,
		Filtered:    res.Filtered,
	}, nil
}

func (c *client) EnsureIndexes(ctx context.Context) error { return c.engine.EnsureIndexes(ctx) }

func (c *client) Close() error { return c.engine.Close() }
