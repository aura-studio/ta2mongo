package daemon

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/source"
)

// Role is the daemon runtime role (role.mode = daemon): the report service tails
// TA logs, filters, resolves identity, and writes to MongoDB, running until
// interrupted.
type Role struct{}

// Run builds the report service from cfg, installs signal handling, and runs the
// pipeline until SIGINT/SIGTERM. The per-module config slicing lives in
// NewFromTree.
func (Role) Run(ctx context.Context, cfg cfgtree.Tree) error {
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Degraded idle mode: an intentionally-empty dao.mongo.uri must not crash
	// the pod. The daemon starts and stays healthy but runs NO logic — no
	// tailer, no pipeline, no Mongo — until it is restarted with a URI. Typed
	// New/NewFromTree are unchanged (library callers still get "uri is
	// required"); this is a role-level policy only.
	if !dao.URIConfigured(cfg) {
		logging.Warn("tango daemon: dao.mongo.uri is empty — running idle, no reporting will happen; " +
			"set dao.mongo.uri (or TANGO_DAO_MONGO_URI) and restart to enable")
		<-ctx.Done()
		logging.Info("tango daemon: idle mode exiting on signal")
		return nil
	}

	svc, err := NewFromTree(ctx, cfg)
	if err != nil {
		logging.WithError(err).Error("tango daemon: init failed")
		return err
	}
	defer func() {
		if err := svc.Shutdown(); err != nil {
			logging.WithError(err).Error("tango daemon: shutdown error")
		}
	}()

	if err := svc.EnsureIndexes(ctx); err != nil {
		logging.WithError(err).Error("tango daemon: ensure indexes failed")
		return err
	}

	return svc.Run(ctx)
}

// NewFromTree builds the daemon Service from the resolved top-level config tree:
// it slices the dao / parser / source / process / cfgsync branches (each module
// owns its own FromTree decode + defaults + validation), enforces the
// daemon-only requirement that a log pattern is configured, logs the startup
// banner, and delegates to New. It is the wiring-layer counterpart to New —
// roles build the service from the whole config with one call, while tests and
// programmatic callers keep using New with typed module configs directly.
func NewFromTree(ctx context.Context, cfg cfgtree.Tree) (*Service, error) {
	daoCfg, err := dao.FromTree(cfg)
	if err != nil {
		return nil, err
	}
	parserCfg, err := parser.FromTree(cfg)
	if err != nil {
		return nil, err
	}
	srcCfg, err := source.FromTree(cfg)
	if err != nil {
		return nil, err
	}
	procCfg, err := process.FromTree(cfg)
	if err != nil {
		return nil, err
	}
	cfgsyncCfg, err := cfgsync.FromTree(cfg)
	if err != nil {
		return nil, err
	}

	if srcCfg.Tailer == nil || len(srcCfg.Tailer.LogPattern) == 0 {
		return nil, fmt.Errorf("config: source.tailer.logPattern is required (at least one regex)")
	}

	logging.WithFields(logging.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": maskURI(daoCfg.Mongo.URI),
		"role":      "daemon",
	}).Info("tango daemon: starting")

	return New(ctx, daoCfg, parserCfg, srcCfg, procCfg, cfgsyncCfg)
}

// maskURI masks the credentials portion of a MongoDB URI for safe logging.
func maskURI(uri string) string {
	i := strings.Index(uri, "://")
	if i < 0 {
		return uri
	}
	rest := uri[i+3:]
	at := strings.IndexByte(rest, '@')
	if at < 0 {
		return uri
	}
	return fmt.Sprintf("%s***:***@%s", uri[:i+3], rest[at+1:])
}
