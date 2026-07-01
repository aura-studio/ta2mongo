package cfgsync

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
)

// pollBackend reads the config document on a fixed interval. It is the default
// backend: it works against any MongoDB topology (including standalone mongod)
// and on DocumentDB without enabling change streams, and its worst-case
// staleness is exactly one pollInterval. Combined with the monotonic version
// guard it is, on its own, enough to satisfy the safety model (bounded staleness
// + self-healing + no-rollback + bad-config-can't-crash).
type pollBackend struct {
	dao *dao.Dao
	cfg *Config
}

// Run does the startup convergence read first (so a fresh process converges to
// the central config before its steady state), then reads on each pollInterval
// tick until ctx is cancelled. A read error is logged and the loop continues —
// the next tick retries, which is the self-healing path for a transient DB blip.
func (b *pollBackend) Run(ctx context.Context, observe func(bson.M) error) error {
	// Startup convergence read: pull once before entering the loop.
	b.readOnce(ctx, observe)

	ticker := time.NewTicker(b.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			b.readOnce(ctx, observe)
		}
	}
}

// readOnce performs one fetch + observe, logging (but swallowing) a read error so
// the poll loop survives transient DB unavailability.
func (b *pollBackend) readOnce(ctx context.Context, observe func(bson.M) error) {
	doc, err := fetchDoc(ctx, b.dao, DefaultCollection, b.cfg.DocumentID)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down; the read failed only because ctx was cancelled
		}
		logging.WithError(err).Warn("cfgsync: poll read failed, will retry next interval")
		return
	}
	// observe applies the version guard; its error (bad config) is already logged
	// inside the Watcher and must not stop polling.
	_ = observe(doc)
}
