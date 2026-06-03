// Package daemon implements the `tango daemon` command: the report service
// tails TA logs, filters, resolves identity, and writes to MongoDB.
package daemon

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/role/daemon"
)

// NewCommand builds the `tango daemon` command. It tails the configured TA
// logs and reports them to MongoDB, running until interrupted.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Daemon report service: tail TA logs, filter, and write to MongoDB",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := resolveConfigPath(configFlag(cmd),
				"daemon.yaml", "daemon.yml", "daemon.json")
			return runDaemonService(cmd, path)
		},
	}
	cmd.Flags().String("dao.mongo.uri", "", "MongoDB connection URI (config key dao.mongo.uri)")
	cmd.Flags().String("logging.level", "", "log level: debug, info, warn, error (config key logging.level)")
	return cmd
}

// configFlag reads the inherited --config persistent flag (empty when unset).
func configFlag(cmd *cobra.Command) string {
	v, _ := cmd.Flags().GetString("config")
	return v
}

// resolveConfigPath returns the config file path to use. When flagVal is set
// (the --config flag) it is returned verbatim. Otherwise the first of the
// candidate filenames that exists in the binary's own directory is returned, so
// each subcommand auto-detects its own default regardless of the current
// working directory. When none exist it returns "" and the loader falls back to
// defaults + env + flags.
func resolveConfigPath(flagVal string, candidates ...string) string {
	if flagVal != "" {
		return flagVal
	}
	dir := "."
	if exe, err := os.Executable(); err == nil {
		dir = filepath.Dir(exe)
	}
	for _, name := range candidates {
		p := filepath.Join(dir, name)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// runDaemonService loads the daemon config from path and runs the
// report service (tail TA logs -> filter -> MongoDB).
func runDaemonService(cmd *cobra.Command, path string) error {
	c, err := config.Load(path, cmd.Flags())
	if err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if len(c.Source.Tailer.LogPattern) == 0 {
		return fmt.Errorf("config: source.tailer.logPattern is required (at least one regex)")
	}
	logging.Init(c.Logging.Level)
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
