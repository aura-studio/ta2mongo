package cmd

import (
	"context"
	"os"
	"runtime"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/daemon"
)

// NewDaemon creates the daemon subcommand.
func NewDaemon() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run as a daemon: tail log files and continuously import into MongoDB",
		RunE:  runDaemon,
	}
}

func runDaemon(cmd *cobra.Command, _ []string) error {
	cfg, logger, ctx, cancel, err := setup(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	return runDaemonWithConfig(cfg, logger, ctx)
}

func runDaemonWithConfig(cfg config.Config, logger *logrus.Logger, ctx context.Context) error {
	pid := os.Getpid()

	logger.WithFields(logrus.Fields{
		"pid":      pid,
		"go_procs": runtime.GOMAXPROCS(0),
		"mongo_uri": maskURI(cfg.Mongo.URI),
	}).Info("daemon: process starting")

	d, err := daemon.New(ctx, cfg, logger)
	if err != nil {
		logger.WithError(err).Error("daemon: failed to initialize, exiting")
		return err
	}
	defer func() {
		logger.WithField("pid", pid).Info("daemon: disconnecting from MongoDB")
		if err := d.Shutdown(); err != nil {
			logger.WithError(err).Error("daemon: error during shutdown")
		}
		logger.WithField("pid", pid).Info("daemon: process exited")
	}()

	logger.Info("daemon: ensuring MongoDB indexes")
	if err := d.EnsureIndexes(ctx); err != nil {
		logger.WithError(err).Error("daemon: failed to ensure indexes, exiting")
		return err
	}
	logger.Info("daemon: indexes ready")

	logger.WithField("pid", pid).Info("daemon: entering main loop, press Ctrl+C to stop")
	err = d.Run(ctx)
	logger.WithField("pid", pid).Info("daemon: main loop exited, shutting down")
	return err
}

// maskURI masks the password portion of a MongoDB URI for safe logging.
func maskURI(uri string) string {
	// Simple masking: find "://" and "@", mask content between them.
	start := -1
	for i := 0; i < len(uri)-2; i++ {
		if uri[i] == ':' && uri[i+1] == '/' && uri[i+2] == '/' {
			start = i + 3
			break
		}
	}
	if start == -1 {
		return uri
	}
	at := -1
	for i := start; i < len(uri); i++ {
		if uri[i] == '@' {
			at = i
			break
		}
	}
	if at == -1 {
		return uri // no credentials in URI
	}
	// Mask everything between "://" and "@" with "***:***"
	return uri[:start] + "***:***" + uri[at:]
}
