package cfgsync

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
)

// resubscribeBackoff is the wait between change-stream re-subscribe attempts
// after the stream breaks at runtime (transient network / failover). It is
// bounded by ctx, so shutdown is still prompt.
const resubscribeBackoff = 2 * time.Second

// changeStreamBackend subscribes to a change stream on the config collection for
// low-latency pushes, with two safety nets layered on top: a subscribe-then-read
// startup (closing the read↔subscribe TOCTOU gap), and a periodic full reconcile
// read that bounds staleness even if the stream silently drops events or breaks.
// Every document — initial read, stream event, and reconcile read — passes
// through the same monotonic version guard in the Watcher.
type changeStreamBackend struct {
	dao *dao.Dao
	cfg *Config
}

// streamEvent is the decoded shape of a change event. Only fullDocument is
// needed: insert/replace/update (with updateLookup) carry the post-image, which
// is fed straight into observe. Delete events carry no fullDocument and decode to
// nil, which observe treats as a no-op (keep current config).
type streamEvent struct {
	FullDocument bson.M `bson:"fullDocument"`
}

// Run opens the stream, does the subscribe-then-snapshot startup, then services
// stream events and reconcile ticks until ctx is cancelled. A topology that does
// not support change streams (e.g. standalone mongod, DocumentDB without
// modifyChangeStreams) fails the initial subscribe with a clear error pointing at
// backend=poll — it is never silently swallowed. A stream that breaks at runtime
// is re-subscribed (with a full-read fallback), not fatal.
func (b *changeStreamBackend) Run(ctx context.Context, observe func(bson.M) error) error {
	cs, err := b.subscribe(ctx)
	if err != nil {
		return fmt.Errorf("cfgsync: change stream unavailable on this topology "+
			"(needs a replica set / DocumentDB modifyChangeStreams; use backend=%s otherwise): %w",
			BackendPoll, err)
	}

	// Subscribe-then-snapshot: now that the stream is live, the full read cannot
	// miss an update that lands between read and subscribe.
	b.readOnce(ctx, observe)

	reconcile := time.NewTicker(b.cfg.ReconcileInterval)
	defer reconcile.Stop()

	for {
		docCh := make(chan bson.M)
		errCh := make(chan error, 1)
		pumpCtx, cancelPump := context.WithCancel(ctx)
		go b.pump(pumpCtx, cs, docCh, errCh)

		broken := false
		for !broken {
			select {
			case <-ctx.Done():
				cancelPump()
				_ = cs.Close(context.Background())
				return ctx.Err()
			case <-reconcile.C:
				// Fallback full read: self-heals dropped events / silent gaps.
				b.readOnce(ctx, observe)
			case doc := <-docCh:
				_ = observe(doc)
			case err := <-errCh:
				logging.WithError(err).Warn("cfgsync: change stream broke, re-subscribing")
				cancelPump()
				_ = cs.Close(context.Background())
				cs, err = b.resubscribe(ctx)
				if err != nil {
					return err // only returns on ctx cancellation
				}
				// Re-read in full after a gap, then restart the pump on the new stream.
				b.readOnce(ctx, observe)
				broken = true
			}
		}
		cancelPump()
	}
}

// subscribe opens a change stream filtered to the tracked document, requesting
// the full post-image so update events carry the whole document (DocumentDB and
// MongoDB both honour updateLookup).
func (b *changeStreamBackend) subscribe(ctx context.Context) (*mongo.ChangeStream, error) {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"documentKey._id": b.cfg.DocumentID}}},
	}
	opts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	return b.dao.Watch(ctx, b.cfg.Collection, pipeline, opts)
}

// resubscribe retries subscribe with a bounded backoff until it succeeds or ctx
// is cancelled. Runtime stream breaks are transient (failover / network), so the
// backend keeps trying rather than crashing.
func (b *changeStreamBackend) resubscribe(ctx context.Context) (*mongo.ChangeStream, error) {
	for {
		cs, err := b.subscribe(ctx)
		if err == nil {
			return cs, nil
		}
		logging.WithError(err).Warn("cfgsync: change stream re-subscribe failed, retrying")
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(resubscribeBackoff):
		}
	}
}

// pump reads events off the stream and forwards each post-image document to
// docCh. It runs in its own goroutine because ChangeStream.Next blocks; observe
// is never called here so the version guard stays single-threaded in the Run
// loop. A stream error (or clean ctx cancellation) ends the pump and is reported
// on errCh.
func (b *changeStreamBackend) pump(ctx context.Context, cs *mongo.ChangeStream, docCh chan<- bson.M, errCh chan<- error) {
	defer logging.Recover("cfgsync change stream pump")
	for cs.Next(ctx) {
		var ev streamEvent
		if err := cs.Decode(&ev); err != nil {
			logging.WithError(err).Warn("cfgsync: failed to decode change event, skipping")
			continue
		}
		if ev.FullDocument == nil {
			continue // delete (or no post-image) → nothing to apply
		}
		select {
		case docCh <- ev.FullDocument:
		case <-ctx.Done():
			return
		}
	}
	// Next returned false: either ctx was cancelled or the stream errored.
	if err := cs.Err(); err != nil {
		errCh <- err
		return
	}
	errCh <- context.Canceled
}

// readOnce performs one full read + observe, logging (but swallowing) a read
// error so the stream loop survives a transient DB blip.
func (b *changeStreamBackend) readOnce(ctx context.Context, observe func(bson.M) error) {
	doc, err := fetchDoc(ctx, b.dao, b.cfg.Collection, b.cfg.DocumentID)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		logging.WithError(err).Warn("cfgsync: reconcile read failed, will retry")
		return
	}
	_ = observe(doc)
}
