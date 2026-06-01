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
	"rocket-nano/tools/tango/internal/filter"
	"rocket-nano/tools/tango/internal/remoteconfig"
)

// newDaemonCmd groups the two daemon run modes under `tango daemon`:
//
//   - standalone: self-contained reporting — tail TA logs → report filter →
//     MongoDB. Local filter; no remote config, no tasks.
//   - agent:      report PLUS sync the filter from the remote-config document
//     and run the task agent (register a heartbeat, claim/execute published
//     report-sync / backfill / sql tasks).
func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Daemon role: standalone (report only) or agent (report + sync + tasks)",
	}
	cmd.AddCommand(newDaemonStandaloneCmd(), newDaemonAgentCmd())
	return cmd
}

// newDaemonStandaloneCmd is `tango daemon standalone`.
func newDaemonStandaloneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "standalone",
		Short: "Standalone reporting: tail TA logs into MongoDB (local filter, no remote config, no tasks)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(cmd, config.DaemonModeStandalone)
		},
	}
}

// newDaemonAgentCmd is `tango daemon agent`.
func newDaemonAgentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent: report + remote-config sync + claim/execute tasks (report-sync / backfill / sql)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(cmd, config.DaemonModeAgent)
		},
	}
	cmd.Flags().String("instanceID", "", "agent instance id (maps to agent.instanceID; required)")
	return cmd
}

func runDaemon(cmd *cobra.Command, mode string) error {
	_, rt, err := config.LoadDaemon(daemonConfigPath(), cmd.Flags(), mode)
	if err != nil {
		return err
	}
	logger := newDaemonLogger(rt)

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
		logger = newDaemonLogger(rt)
	}

	logger.WithFields(logrus.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": maskURI(rt.Mongo.URI),
		"mode":      mode,
	}).Info("tango daemon: starting")

	return runReport(ctx, rt, logger, agentOn)
}

// runReport runs the reporting pipeline. In agent mode it additionally starts
// the in-process agent alongside it, sharing the live reporting filter so a
// report-sync task hot-swaps it without a restart.
func runReport(ctx context.Context, rt config.Config, logger *logrus.Logger, agentOn bool) error {
	d, err := daemon.New(ctx, rt, logger)
	if err != nil {
		logger.WithError(err).Error("tango daemon: init failed")
		return err
	}
	defer func() {
		if err := d.Shutdown(); err != nil {
			logger.WithError(err).Error("tango daemon: shutdown error")
		}
	}()

	if err := d.EnsureIndexes(ctx); err != nil {
		logger.WithError(err).Error("tango daemon: ensure indexes failed")
		return err
	}

	if agentOn {
		stop, err := startAgent(ctx, rt, logger, d.Filter())
		if err != nil {
			return err
		}
		defer stop()
	}

	return d.Run(ctx)
}

// daemonConfigPath resolves the daemon config file: the shared --config flag if
// set, otherwise the auto-detected default next to the binary (see
// defaultConfigPath).
func daemonConfigPath() string {
	if configFile != "" {
		return configFile
	}
	return defaultConfigPath()
}

// newAgent constructs an agent and ensures its indexes. When flt is non-nil the
// agent shares the hosting daemon's live reporting filter so a report-sync task
// can hot-swap it without a restart.
func newAgent(ctx context.Context, rt config.Config, logger *logrus.Logger, flt *filter.Holder) (*agent.Agent, error) {
	a, err := agent.New(ctx, rt, logger)
	if err != nil {
		logger.WithError(err).Error("tango daemon: agent init failed")
		return nil, err
	}
	if flt != nil {
		a.AttachReportingFilter(flt)
	}
	if err := a.EnsureIndexes(ctx); err != nil {
		_ = a.Shutdown()
		logger.WithError(err).Error("tango daemon: agent ensure indexes failed")
		return nil, err
	}
	return a, nil
}

// startAgent constructs and runs the in-process agent alongside the reporting
// pipeline, returning a cleanup func that shuts it down. The agent runs in its
// own goroutine; daemon.Run blocks the main goroutine until ctx is cancelled,
// at which point the agent's Run also returns.
func startAgent(ctx context.Context, rt config.Config, logger *logrus.Logger, flt *filter.Holder) (func(), error) {
	a, err := newAgent(ctx, rt, logger, flt)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := a.Run(ctx); err != nil {
			logger.WithError(err).Error("tango daemon: agent run error")
		}
	}()
	return func() {
		<-done
		if err := a.Shutdown(); err != nil {
			logger.WithError(err).Error("tango daemon: agent shutdown error")
		}
	}, nil
}

func newDaemonLogger(cfg config.Config) *logrus.Logger {
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
