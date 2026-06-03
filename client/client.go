// Package client provides a Redis-client-style API for writing ThinkingData
// records to MongoDB. It manages connection pooling internally and exposes
// simple, synchronous methods for ingesting JSON log lines.
//
// Usage is similar to a Redis or database client library:
//
//	cli, err := client.New(ctx,
//	    client.WithURI("mongodb://localhost:27017/tango"),
//	)
//	if err != nil { ... }
//	defer cli.Close()
//
//	// Single line
//	err = cli.Ingest(ctx, `{"#type":"track","#event_name":"login",...}`)
//
//	// Batch
//	err = cli.IngestBatch(ctx, lines)
//
// The Client is safe for concurrent use from multiple goroutines.
// The underlying MongoDB driver manages a connection pool automatically.
package client

import (
	"context"
	"fmt"
	"time"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/dao"
	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process"
)

// Options configures the Client connection and behavior.
type Options struct {
	// URI is the MongoDB connection string (required).
	// Must contain the database name as the URI path, e.g.
	// mongodb://host:27017/tango
	URI string

	// MaxElapsedTime is the maximum retry duration for bulk writes.
	// Default: 10s
	MaxElapsedTime time.Duration

	// BatchSize is the maximum number of write models per bulk write call
	// when using IngestBatch. Default: 1000
	BatchSize int

	// FilterInclude / FilterExclude are optional reporting-filter expressions
	// applied to Ingest / IngestBatch (string upload). Empty = pass everything.
	FilterInclude []string
	FilterExclude []string
}

// Option is a functional option for building a Client.
type Option func(*Options)

// WithURI sets the MongoDB connection URI.
func WithURI(uri string) Option {
	return func(o *Options) { o.URI = uri }
}

// WithMaxElapsedTime sets the maximum retry duration for bulk writes.
func WithMaxElapsedTime(d time.Duration) Option {
	return func(o *Options) { o.MaxElapsedTime = d }
}

// WithBatchSize sets the maximum number of write models per bulk write call.
func WithBatchSize(n int) Option {
	return func(o *Options) { o.BatchSize = n }
}

// WithFilter sets the reporting-filter expressions applied to string uploads
// (Ingest / IngestBatch).
func WithFilter(include, exclude []string) Option {
	return func(o *Options) {
		o.FilterInclude = include
		o.FilterExclude = exclude
	}
}

func (o *Options) defaults() {
	if o.MaxElapsedTime <= 0 {
		o.MaxElapsedTime = 10 * time.Second
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 1000
	}
}

// Client is a connection-pool-backed client for writing ThinkingData records
// to MongoDB. It is safe for concurrent use.
type Client struct {
	ingester *process.Ingester
	dao      *dao.Dao
	opts     Options
}

/*
New creates a Client, connecting to MongoDB and initializing the connection pool.

Example:

	cli, err := client.New(ctx,
	    client.WithURI("mongodb://localhost:27017/tango"),
	    client.WithBatchSize(1000),
	)

The caller must call Close when done.
*/
func New(ctx context.Context, optFns ...Option) (*Client, error) {
	opts := Options{}
	for _, fn := range optFns {
		if fn != nil {
			fn(&opts)
		}
	}

	if opts.URI == "" {
		return nil, fmt.Errorf("client: URI is required")
	}
	opts.defaults()

	// Build a minimal config for the store/ingester (only retry + filter
	// settings matter; the synchronous ingester does not use the daemon's batch
	// flusher, so no pipeline section is needed).
	cfg := config.Config{
		Dao: &dao.Config{
			Mongo: &daomongo.Config{URI: opts.URI},
			Store: &store.Config{MaxElapsedTime: opts.MaxElapsedTime},
		},
		Parser: &parser.Config{Filter: &filter.Config{Include: opts.FilterInclude, Exclude: opts.FilterExclude}},
	}

	ig, err := process.NewIngester(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}

	return &Client{
		ingester: ig,
		dao:      ig.Dao(),
		opts:     opts,
	}, nil
}

// Close disconnects from MongoDB and releases all resources.
func (c *Client) Close() error {
	return c.dao.Mongo.Close()
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
// Call this once during application startup.
func (c *Client) EnsureIndexes(ctx context.Context) error {
	return c.dao.Store.EnsureIndexes(ctx)
}

// Ingest parses a single JSON log line, resolves user identity, and writes
// the result to MongoDB. It blocks until the write completes or fails.
//
// Returns nil on success, or an error describing the failure.
func (c *Client) Ingest(ctx context.Context, line string) error {
	return c.ingester.Ingest(ctx, line)
}

// IngestBatch processes multiple JSON log lines in a single batch.
// Lines that fail to parse are sent to dead_letter. The batch is flushed
// in a single bulk write per collection.
//
// Returns an error if any MongoDB bulk write fails. Parse errors for
// individual lines are logged but do not block other lines.
func (c *Client) IngestBatch(ctx context.Context, lines []string) error {
	return c.ingester.IngestBatch(ctx, lines)
}

// Ping verifies that the MongoDB connection is alive.
func (c *Client) Ping(ctx context.Context) error {
	return c.dao.Mongo.Client.Ping(ctx, nil)
}

// Stats returns the cumulative write statistics (retry counts).
func (c *Client) Stats() *store.WriteStats {
	return c.dao.Store.Stats()
}
