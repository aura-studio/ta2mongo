// Package client provides a Redis-client-style API for writing ThinkingData
// records to MongoDB. It manages connection pooling internally and exposes
// simple, synchronous methods for ingesting JSON log lines.
//
// Usage is similar to a Redis or database client library:
//
//	cli, err := client.New(ctx,
//	    client.WithURI("mongodb://localhost:27017/ta2mongo"),
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

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/ingest"
	"rocket-nano/tools/ta2mongo/store"
)

// Options configures the Client connection and behavior.
type Options struct {
	// URI is the MongoDB connection string (required).
	// Must contain the database name as the URI path, e.g.
	// mongodb://host:27017/ta2mongo
	URI string

	// MaxElapsedTime is the maximum retry duration for bulk writes.
	// Default: 10s
	MaxElapsedTime time.Duration

	// BatchSize is the maximum number of write models per bulk write call
	// when using IngestBatch. Default: 1000
	BatchSize int

	// Logger is an optional logrus logger. If nil, a default logger is created
	// at Info level.
	Logger *logrus.Logger
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

// WithLogger sets the logger instance.
func WithLogger(l *logrus.Logger) Option {
	return func(o *Options) { o.Logger = l }
}

func (o *Options) defaults() {
	if o.MaxElapsedTime <= 0 {
		o.MaxElapsedTime = 10 * time.Second
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 1000
	}
	if o.Logger == nil {
		l := logrus.New()
		l.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
		l.SetLevel(logrus.InfoLevel)
		o.Logger = l
	}
}

// Client is a connection-pool-backed client for writing ThinkingData records
// to MongoDB. It is safe for concurrent use.
type Client struct {
	ingester *ingest.Ingester
	client   *mongo.Client
	store    *store.Store
	logger   *logrus.Logger
	opts     Options
}

/*
New creates a Client, connecting to MongoDB and initializing the connection pool.

Example:

	cli, err := client.New(ctx,
	    client.WithURI("mongodb://localhost:27017/ta2mongo"),
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

	dbName, err := config.MongoDBFromURI(opts.URI)
	if err != nil {
		return nil, fmt.Errorf("client: URI must contain database name in path (e.g. mongodb://host:27017/ta2mongo): %w", err)
	}

	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(opts.URI))
	if err != nil {
		return nil, fmt.Errorf("client: connect to mongo: %w", err)
	}

	// Ping to verify connection.
	if err := mongoClient.Ping(ctx, nil); err != nil {
		_ = mongoClient.Disconnect(ctx)
		return nil, fmt.Errorf("client: ping mongo: %w", err)
	}

	db := mongoClient.Database(dbName)

	// Build a minimal config for the store (only retry settings matter).
	// We provide batch tuning defaults; ingest itself doesn't use the daemon batch flusher.
	cfg := config.Config{
		Mongo: config.MongoConfig{URI: opts.URI},
		Retry: config.RetryConfig{MaxElapsedTime: opts.MaxElapsedTime},
		Batch: config.BatchConfig{
			SizeMin:         1,
			SizeInitial:     opts.BatchSize,
			SizeMax:         opts.BatchSize,
			WorkerCount:     1,
			WorkerChSize:    1,
			FlushIntervalMs: 1000,
		},
	}

	st := store.New(db, cfg, opts.Logger)
	ig := ingest.NewFromClient(mongoClient, cfg, opts.Logger)

	return &Client{
		ingester: ig,
		client:   mongoClient,
		store:    st,
		logger:   opts.Logger,
		opts:     opts,
	}, nil
}

// Close disconnects from MongoDB and releases all resources.
func (c *Client) Close() error {
	return c.client.Disconnect(context.Background())
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
// Call this once during application startup.
func (c *Client) EnsureIndexes(ctx context.Context) error {
	return c.store.EnsureIndexes(ctx)
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
	return c.client.Ping(ctx, nil)
}

// Stats returns the cumulative write statistics (retry counts).
func (c *Client) Stats() *store.WriteStats {
	return c.store.Stats()
}
