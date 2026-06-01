// Package daemon implements the `tango daemon` subcommand tree: the two daemon
// run modes, standalone and cluster.
package daemon

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
	svcdaemon "rocket-nano/tools/tango/internal/service/daemon"
)

// NewCommand builds the `tango daemon` parent command with its two run modes,
// both of which tail TA logs → apply the report filter → write to MongoDB:
//
//   - standalone: local filter only; no remote config. Default config file:
//     standalone.{yaml,yml,json} next to the binary.
//   - cluster:    additionally syncs the report filter from a MongoDB
//     control-plane document (hot-reload). Default config file:
//     cluster.{yaml,yml,json} next to the binary.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Daemon role: standalone (local filter) or cluster (report + remote-config sync)",
	}
	// Viper-native hierarchical overrides shared by both modes: the flag name is
	// the full config key.
	cmd.PersistentFlags().String("generic.mongo.uri", "", "MongoDB connection URI (config key generic.mongo.uri)")
	cmd.PersistentFlags().String("generic.logging.level", "", "log level: debug, info, warn, error (config key generic.logging.level)")
	cmd.AddCommand(newStandaloneCmd(), newClusterCmd())
	return cmd
}

func newStandaloneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "standalone",
		Short: "Standalone reporting: tail TA logs into MongoDB (local filter, no remote config)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := cli.ResolveConfigPath(configFlag(cmd), "standalone.yaml", "standalone.yml", "standalone.json")
			return runDaemon(cmd, config.DaemonModeStandalone, path)
		},
	}
}

func newClusterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "cluster",
		Short: "Cluster reporting: tail TA logs into MongoDB + sync the report filter from a control-plane document",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := cli.ResolveConfigPath(configFlag(cmd), "cluster.yaml", "cluster.yml", "cluster.json")
			return runDaemon(cmd, config.DaemonModeCluster, path)
		},
	}
}

// configFlag reads the inherited --config persistent flag (empty when unset).
func configFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("config")
	return v
}

func runDaemon(cmd *cobra.Command, mode, path string) error {
	_, rt, err := config.LoadDaemon(path, cmd.Flags(), mode)
	if err != nil {
		return err
	}
	logger := cli.NewLogger(rt.Logging.Level)

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Cluster mode syncs the reporting filter from the remote-config document:
	// apply the override once at startup, then daemon.Run keeps it hot-reloaded.
	// Standalone keeps the local filter verbatim.
	if mode == config.DaemonModeCluster {
		rt, err = remoteconfig.ApplyAtStartup(ctx, rt, logger)
		if err != nil {
			return err
		}
		logger = cli.NewLogger(rt.Logging.Level)
	}

	logger.WithFields(logrus.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": maskURI(rt.Mongo.URI),
		"mode":      mode,
	}).Info("tango daemon: starting")

	return runReport(ctx, rt, logger)
}

// runReport runs the reporting pipeline until ctx is cancelled. In cluster mode
// the daemon's own sync loop (enabled via rt.RemoteConfig.Enabled) hot-reloads
// the filter from the control-plane document.
func runReport(ctx context.Context, rt config.Config, logger *logrus.Logger) error {
	d, err := svcdaemon.New(ctx, rt, logger)
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
	return d.Run(ctx)
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
