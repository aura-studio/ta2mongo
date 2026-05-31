// Command tangod is the tango daemon: it tails ThinkingData log files, applies
// the reporting (upload) filter, and writes records to MongoDB. When the agent
// feature is enabled (agent.enabled in daemon.{yaml,json}) it additionally runs
// the in-process task agent: registering a heartbeat, claiming published tasks
// (report-sync / backfill / sql), and reporting results.
package main

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
	"rocket-nano/tools/tango/internal/agent"
	"rocket-nano/tools/tango/internal/daemon"
	"rocket-nano/tools/tango/internal/remoteconfig"
)

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	var configFile string
	cmd := &cobra.Command{
		Use:   "tangod",
		Short: "Tango daemon: tail TA logs into MongoDB, optionally hosting the task agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return run(cmd, configFile)
		},
	}
	cmd.Flags().StringVar(&configFile, "config", "daemon.yaml",
		"path to daemon config file (.yaml/.yml/.json; skipped if absent)")
	cmd.Flags().String("mongoURI", "", "MongoDB connection URI (maps to mongo.uri)")
	cmd.Flags().String("logLevel", "", "log level: debug, info, warn, error")
	cmd.Flags().String("instanceID", "", "agent instance id (maps to agent.instanceID)")
	return cmd
}

func run(cmd *cobra.Command, configFile string) error {
	dc, rt, err := config.LoadDaemon(configFile, cmd.Flags())
	if err != nil {
		return err
	}
	logger := newLogger(rt)

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Apply the remote-config override (reporting filter) at startup.
	rt, err = remoteconfig.ApplyAtStartup(ctx, rt, logger)
	if err != nil {
		return err
	}
	logger = newLogger(rt)

	logger.WithFields(logrus.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": maskURI(rt.Mongo.URI),
		"agent":     dc.Agent.Enabled,
	}).Info("tangod: starting")

	d, err := daemon.New(ctx, rt, logger)
	if err != nil {
		logger.WithError(err).Error("tangod: daemon init failed")
		return err
	}
	defer func() {
		if err := d.Shutdown(); err != nil {
			logger.WithError(err).Error("tangod: daemon shutdown error")
		}
	}()

	if err := d.EnsureIndexes(ctx); err != nil {
		logger.WithError(err).Error("tangod: ensure indexes failed")
		return err
	}

	// Optional agent feature: run it alongside the reporting pipeline, sharing
	// the daemon's live reporting filter so a report-sync task hot-swaps it.
	if dc.Agent.Enabled {
		stop, err := startAgent(ctx, rt, logger, d)
		if err != nil {
			return err
		}
		defer stop()
	}

	return d.Run(ctx)
}

// startAgent constructs and runs the in-process agent, returning a cleanup func
// that shuts it down. The agent runs in its own goroutine; daemon.Run blocks
// the main goroutine until ctx is cancelled, at which point the agent's Run
// also returns.
func startAgent(ctx context.Context, rt config.Config, logger *logrus.Logger, d *daemon.Daemon) (func(), error) {
	a, err := agent.New(ctx, rt, logger)
	if err != nil {
		logger.WithError(err).Error("tangod: agent init failed")
		return nil, err
	}
	a.AttachReportingFilter(d.Filter())
	if err := a.EnsureIndexes(ctx); err != nil {
		_ = a.Shutdown()
		logger.WithError(err).Error("tangod: agent ensure indexes failed")
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := a.Run(ctx); err != nil {
			logger.WithError(err).Error("tangod: agent run error")
		}
	}()
	return func() {
		<-done
		if err := a.Shutdown(); err != nil {
			logger.WithError(err).Error("tangod: agent shutdown error")
		}
	}, nil
}

func newLogger(cfg config.Config) *logrus.Logger {
	l := logrus.New()
	l.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	level, err := logrus.ParseLevel(strings.ToLower(cfg.Logging.Level))
	if err != nil {
		level = logrus.InfoLevel
	}
	l.SetLevel(level)
	return l
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
