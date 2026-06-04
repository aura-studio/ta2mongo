// Package daemon implements the daemon runtime role (role.mode = daemon): the
// report service tails TA logs, filters, resolves identity, and writes to MongoDB.
package daemon

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/role/daemon"
)

// Run executes the daemon role against an already-loaded config: it tails the
// configured TA logs and reports them to MongoDB, running until interrupted.
// The runtime role is selected by config key role.mode, dispatched from main.
func Run(cmd *cobra.Command, c *config.Config) error {
	if len(c.Source.Tailer.LogPattern) == 0 {
		return fmt.Errorf("config: source.tailer.logPattern is required (at least one regex)")
	}
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logging.WithFields(logging.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": maskURI(c.Dao.Mongo.URI),
		"role":      "daemon",
	}).Info("tango daemon: starting")

	return runReport(ctx, c)
}

// runReport runs the reporting pipeline and blocks until ctx is cancelled.
func runReport(ctx context.Context, c *config.Config) error {
	svc, err := daemon.New(ctx, c.Dao, c.Parser, c.Source.Tailer, c.Process)
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
