package shared

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/cli"
	"rocket-nano/tools/tango/internal/core/remoteconfig"
	"rocket-nano/tools/tango/internal/service/report"
	"rocket-nano/tools/tango/internal/service/worker"
)

// RunReportService loads the report config from path and runs the report
// service (tail TA logs -> filter -> MongoDB), with optional remote-config
// hot-reload.
func RunReportService(cmd *cobra.Command, path string) error {
	_, rt, err := config.LoadReport(path, cmd.Flags())
	if err != nil {
		return err
	}
	logger := cli.NewLogger(rt.Logging.Level)
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logger.WithFields(logrus.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": MaskURI(rt.Mongo.URI),
		"role":      "report",
	}).Info("tango report: starting")

	return runReport(ctx, rt, logger, false)
}

// RunWorkerService loads the worker config from path and runs the task worker
// service (claim/execute report-sync / backfill / sql tasks).
func RunWorkerService(cmd *cobra.Command, path string) error {
	_, rt, err := config.LoadWorker(path, cmd.Flags())
	if err != nil {
		return err
	}
	logger := cli.NewLogger(rt.Logging.Level)
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logger.WithFields(logrus.Fields{
		"pid":        os.Getpid(),
		"go_procs":   runtime.GOMAXPROCS(0),
		"mongo_uri":  MaskURI(rt.Mongo.URI),
		"role":       "worker",
		"instanceID": rt.InstanceID,
	}).Info("tango worker: starting")

	return runWorker(ctx, rt, logger)
}

// RunDaemon runs the legacy combined daemon for the given mode
// (config.DaemonModeStandalone / DaemonModeAgent). It is used by the legacy
// `daemon` command and the compatibility `profile managed` command. Standalone
// runs report only; agent additionally syncs the filter from remote config and
// runs the in-process worker.
func RunDaemon(cmd *cobra.Command, mode, path string) error {
	_, rt, err := config.LoadDaemon(path, cmd.Flags(), mode)
	if err != nil {
		return err
	}
	logger := cli.NewLogger(rt.Logging.Level)

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	agentOn := mode == config.DaemonModeAgent

	// Agent mode syncs the reporting filter from the remote-config document;
	// apply the override once at startup. Standalone keeps the local filter.
	if agentOn {
		rt, err = remoteconfig.ApplyAtStartup(ctx, rt, logger)
		if err != nil {
			return err
		}
		logger = cli.NewLogger(rt.Logging.Level)
	}

	logger.WithFields(logrus.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": MaskURI(rt.Mongo.URI),
		"mode":      mode,
	}).Info("tango daemon: starting")

	return runReport(ctx, rt, logger, agentOn)
}

// runReport runs the reporting pipeline. In agent mode it additionally starts
// the in-process worker alongside it, sharing the live reporting filter so a
// report-sync task hot-swaps it without a restart.
func runReport(ctx context.Context, rt config.Config, logger *logrus.Logger, agentOn bool) error {
	svc, err := report.New(ctx, rt, logger)
	if err != nil {
		logger.WithError(err).Error("tango report: init failed")
		return err
	}
	defer func() {
		if err := svc.Shutdown(); err != nil {
			logger.WithError(err).Error("tango report: shutdown error")
		}
	}()

	if err := svc.EnsureIndexes(ctx); err != nil {
		logger.WithError(err).Error("tango report: ensure indexes failed")
		return err
	}

	if agentOn {
		stop, err := startWorker(ctx, rt, logger)
		if err != nil {
			return err
		}
		defer stop()
	}

	return svc.Run(ctx)
}

// startWorker constructs and runs the in-process worker alongside the reporting
// pipeline, returning a cleanup func that shuts it down. The worker runs in its
// own goroutine; report.Run blocks the main goroutine until ctx is cancelled,
// at which point the worker's Run also returns.
//
// The worker and report service are fully decoupled: a report-sync task only
// writes the remote-config document, and the co-located report service picks it
// up through its own remote-config sync loop (enabled in agent/managed mode) —
// there is no shared in-process filter holder.
func startWorker(ctx context.Context, rt config.Config, logger *logrus.Logger) (func(), error) {
	w, err := worker.New(ctx, rt, logger)
	if err != nil {
		logger.WithError(err).Error("tango daemon: worker init failed")
		return nil, err
	}
	if err := w.EnsureIndexes(ctx); err != nil {
		_ = w.Shutdown()
		logger.WithError(err).Error("tango daemon: worker ensure indexes failed")
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := w.Run(ctx); err != nil {
			logger.WithError(err).Error("tango daemon: worker run error")
		}
	}()
	return func() {
		<-done
		if err := w.Shutdown(); err != nil {
			logger.WithError(err).Error("tango daemon: worker shutdown error")
		}
	}, nil
}

// runWorker runs a standalone task worker service.
func runWorker(ctx context.Context, rt config.Config, logger *logrus.Logger) error {
	w, err := worker.New(ctx, rt, logger)
	if err != nil {
		logger.WithError(err).Error("tango worker: init failed")
		return err
	}
	defer func() {
		if err := w.Shutdown(); err != nil {
			logger.WithError(err).Error("tango worker: shutdown error")
		}
	}()
	if err := w.EnsureIndexes(ctx); err != nil {
		logger.WithError(err).Error("tango worker: ensure indexes failed")
		return err
	}
	return w.Run(ctx)
}

// MaskURI masks the credentials portion of a MongoDB URI for safe logging.
func MaskURI(uri string) string {
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
