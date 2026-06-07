// Package cfgsync is the runtime dynamic-configuration sync domain. It is the
// dynamic counterpart to cfgtree (the static config carrier): cfgtree loads
// config once at startup, cfgsync keeps a small, explicitly allowlisted slice of
// it converging at runtime against a central document in MongoDB.
//
// cfgsync is not a role. Like api.Engine it is an embeddable Watcher/Service,
// owned by the long-running roles that hold a live reporting filter — daemon
// (tailer → pipeline) and gateway (/upload). The one-shot cli and the library
// api do not embed the read side; worker / taskqueue are unrelated and never
// touch it.
//
// # Read side (this Watcher) and write side (Publish)
//
// The Watcher watches the central config document (collection _tango_config) and
// hot-replaces the live config via injected appliers (the Registry). Publish (see
// publish.go) is the write side: gateway / cli / api all call the one
// cfgsync.Publish, which validates against the same allowlist and bumps a
// monotonic version. Both sides live in this root package and share the document
// schema {_id, version, <subtree>: {...}} and the monotonic-version semantics.
//
// # Safety model
//
// The goal is not instantaneous cluster-wide consistency (physically impossible
// in a distributed system) but bounded staleness + self-healing + no-rollback +
// bad-config-can't-crash, achieved by layering: a convergence read at startup;
// an optional change stream for low latency; a periodic full read as a safety
// net; a monotonic version guard that drops older/replayed documents; and
// validate-before-swap that keeps the last-good value when a new config fails to
// compile. Consumers that need a consistent point-in-time snapshot (bounded
// backfill tasks) do not subscribe at all.
//
// # Dependency boundary
//
// cfgsync reaches MongoDB only through the dao root package façade
// (dao.EJSON / dao.Watch) and the parser only through the parser root façade
// (parser.SwapFilter, wired by the embedding role) — never the dao/ejson or
// parser/filter subpackages. It is DocumentDB-safe: findOne, watch, and
// updateOne($set+$inc) upsert only, no pipeline updates.
package cfgsync

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
)

// Watcher is the read side of cfgsync: it drives the configured backend and feeds
// every config document through the monotonic version guard into onChange. It is
// embedded by the long-running roles (daemon, gateway) and run as a goroutine.
//
// A Watcher is single-threaded by construction: the backend calls observe from
// one goroutine, so lastVersion needs no synchronisation. The downstream apply
// (parser's filter Holder) is itself atomic, so the hot path stays lock-free.
type Watcher struct {
	dao      *dao.Dao
	cfg      *Config
	onChange func(bson.M) error

	// lastVersion is the highest document version accepted so far; it is the
	// monotonic guard that drops stale or replayed documents. Touched only from
	// the backend goroutine via observe.
	lastVersion int64
}

// New builds a Watcher. onChange is the injected applier (typically a
// *Registry's Apply): cfgsync itself is filter-agnostic and only takes the
// document + detects change + invokes onChange; how a document is interpreted and
// applied is owned by the embedding role. A nil onChange makes the Watcher a
// read-only no-op (every document is fetched and version-guarded but nothing is
// applied), which is convenient for tests.
func New(d *dao.Dao, cfg *Config, onChange func(bson.M) error) *Watcher {
	return &Watcher{dao: d, cfg: cfg, onChange: onChange}
}

// Run selects the backend and runs it until ctx is cancelled, returning the
// backend's error (nil on clean ctx cancellation). The backend performs the
// startup convergence read before entering its steady-state loop, so a fresh
// process converges to the central config before doing real work. Panics are not
// recovered here; the embedding role runs Run under its own logging.Recover, in
// line with the daemon's other long-running goroutines.
func (w *Watcher) Run(ctx context.Context) error {
	backend, err := selectBackend(w.dao, w.cfg)
	if err != nil {
		return err
	}
	logging.WithFields(logging.Fields{
		"backend":    w.cfg.Backend,
		"collection": w.cfg.Collection,
		"documentID": w.cfg.DocumentID,
	}).Info("cfgsync: watcher starting")
	return backend.Run(ctx, w.observe)
}

// observe is the version-guarded entry point the backends call for every document
// they read. A missing document (nil) is a no-op (config not published yet → keep
// current). The monotonic version guard drops any document whose version is not
// strictly greater than the highest accepted so far, which makes the watcher
// immune to replays and out-of-order delivery (no rollback). Only after a
// document is accepted is onChange invoked, so an unchanged document never
// triggers a redundant swap. An onChange error is returned for the caller to
// count but does not advance/retreat the guard or stop the backend — the last-good
// value stays live (bad config can't crash).
func (w *Watcher) observe(doc bson.M) error {
	if doc == nil {
		return nil
	}
	version, ok := docVersion(doc)
	if !ok {
		logging.WithField("documentID", w.cfg.DocumentID).
			Warn("cfgsync: config document has no numeric version, ignoring")
		return nil
	}
	if version <= w.lastVersion {
		return nil // stale or replayed → drop (no rollback, no redundant swap)
	}
	w.lastVersion = version
	if w.onChange == nil {
		return nil
	}
	if err := w.onChange(doc); err != nil {
		logging.WithError(err).WithField("version", version).
			Warn("cfgsync: applying config document failed, keeping last-good")
		return err
	}
	logging.WithField("version", version).Info("cfgsync: applied config document")
	return nil
}
