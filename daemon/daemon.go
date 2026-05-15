// Package daemon orchestrates the ta2mongo pipeline:
// file tailing -> line parsing -> batch accumulation -> MongoDB bulk writes.
package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/internal/pipeline"
	"rocket-nano/tools/ta2mongo/store"
	"rocket-nano/tools/ta2mongo/tailer"
	"rocket-nano/tools/ta2mongo/talog"
)

// Daemon is the main runtime that connects all components together.
type Daemon struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.Store
	parser *talog.Parser
	client *mongo.Client
}

// New connects to MongoDB and creates a ready-to-run Daemon.
// The caller must call Shutdown after Run returns to disconnect from MongoDB.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Daemon, error) {
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Mongo.URI))
	if err != nil {
		return nil, err
	}

	dbName, err := config.MongoDBFromURI(cfg.Mongo.URI)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("daemon: %w", err)
	}
	db := client.Database(dbName)
	st := store.New(db, cfg, logger)
	p := talog.NewParser()

	return &Daemon{cfg: cfg, logger: logger, store: st, parser: p, client: client}, nil
}

// Shutdown disconnects the MongoDB client. It must be called after Run returns
// to ensure all final flushes complete before the connection is closed.
func (d *Daemon) Shutdown() error {
	return d.client.Disconnect(context.Background())
}

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (d *Daemon) EnsureIndexes(ctx context.Context) error {
	return d.store.EnsureIndexes(ctx)
}

// Run starts the daemon pipeline and blocks until ctx is cancelled.
//
// Flow: tailer -> lineCh -> dispatcher (routes by user affinity) -> workerCh[i] -> worker_i -> MongoDB
//
// The dispatcher extracts #account_id (preferred) or #distinct_id from each line
// and consistently hashes it to a fixed worker. This guarantees that all operations
// for the same user are processed sequentially by a single worker, preventing
// out-of-order overwrites across workers.
func (d *Daemon) Run(ctx context.Context) error {
	if len(d.cfg.TA.LogPattern) == 0 {
		return errors.New("daemon: ta.logPattern is required (at least one regex)")
	}

	// Start the tailer; it returns a channel of log lines.
	t := tailer.New(d.cfg.TA.LogPattern, d.cfg.RescanInterval(), d.logger)
	lineCh := t.Run(ctx)

	pipeline.RunWorkers(ctx, d.cfg, d.store, d.parser, d.logger, lineCh, pipeline.NoopStats{})
	return nil
}
