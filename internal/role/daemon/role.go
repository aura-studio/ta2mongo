package daemon

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"rocket-nano/tools/tango/internal/cfgtree"
	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/source"
)

// Role is the daemon runtime role (role.mode = daemon): the report service tails
// TA logs, filters, resolves identity, and writes to MongoDB, running until
// interrupted.
type Role struct{}

// Run slices the dao / parser / source / process branches it needs from cfg,
// builds the report service, installs signal handling, and runs the pipeline
// until SIGINT/SIGTERM.
func (Role) Run(ctx context.Context, cfg cfgtree.Tree) error {
	daoCfg, err := dao.FromTree(cfg)
	if err != nil {
		return err
	}
	parserCfg, err := parser.FromTree(cfg)
	if err != nil {
		return err
	}
	srcCfg, err := source.FromTree(cfg)
	if err != nil {
		return err
	}
	procCfg, err := process.FromTree(cfg)
	if err != nil {
		return err
	}
	if len(srcCfg.Tailer.LogPattern) == 0 {
		return fmt.Errorf("config: source.tailer.logPattern is required (at least one regex)")
	}

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logging.WithFields(logging.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": maskURI(daoCfg.Mongo.URI),
		"role":      "daemon",
	}).Info("tango daemon: starting")

	svc, err := New(ctx, daoCfg, parserCfg, srcCfg.Tailer, procCfg)
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
