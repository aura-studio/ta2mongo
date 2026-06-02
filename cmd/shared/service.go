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
// service (tail TA logs -> filter -> MongoDB). When remoteConfig.enabled is
// set, the filter is merged from the remote-config document once at startup and
// then hot-reloaded on each sync tick.
func RunReportService(cmd *cobra.Command, path string) error {
	_, rt, err := config.LoadReport(path, cmd.Flags())
	if err != nil {
		return err
	}
	logger := cli.NewLogger(rt.Logging.Level)
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Apply the remote-config override once at startup so the service begins
	// with the latest filter rather than waiting for the first sync tick.
	if rt.RemoteConfig.Enabled {
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
		"role":      "report",
	}).Info("tango report: starting")

	return runReport(ctx, rt, logger)
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

// runReport runs the reporting pipeline and blocks until ctx is cancelled.
func runReport(ctx context.Context, rt config.Config, logger *logrus.Logger) error {
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

	return svc.Run(ctx)
}

// runWorker runs a task worker service and blocks until ctx is cancelled.
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
