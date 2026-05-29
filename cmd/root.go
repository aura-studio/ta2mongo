// Package cmd defines the CLI commands for tango.
package cmd

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/remoteconfig"
)

var configFile string

// NewRoot creates and returns the root cobra command.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "tango",
		Short: "Tail ThinkingData log files and import them into MongoDB",
	}

	root.PersistentFlags().StringVar(&configFile, "config", "tango.yaml",
		"path to YAML config file (skipped if file does not exist)")

	root.AddCommand(NewDaemon())
	root.AddCommand(NewOnce())
	root.AddCommand(NewIngest())
	root.AddCommand(NewBackfill())

	return root
}

// Execute runs the root command.
func Execute() {
	root := NewRoot()
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// addCommonFlags registers all shared config flags onto a flag set.
// Flag names match YAML keys and TANGO_* env var suffixes exactly.
func addCommonFlags(flags *pflag.FlagSet) {
	flags.String("mongoURI", "", "MongoDB connection URI (required)")
	flags.StringSlice("logPattern", nil, "regex patterns for log file paths (repeatable)")
	flags.Duration("rescanInterval", 0, "how often to rescan for new log files (default 30s)")
	flags.Int("batchSize", 0, "target batch size; min=batchSize/4, max=batchSize*2 (default 1000)")
	flags.Int("batchWorkers", 0, "number of parallel write workers (default 2)")
	flags.Duration("flushInterval", 0, "how often workers flush partial batches (default 1s)")
	flags.Duration("maxElapsedTime", 0, "max total retry time for bulk writes (default 10s)")
	flags.String("logLevel", "", "log level: debug, info, warn, error (default info)")
	flags.String("tailMode", "", "file-tailing strategy: poll or event (default poll)")
	flags.StringSlice("filterInclude", nil, "expr-lang expressions; record is kept only if any evaluates true (repeatable)")
	flags.StringSlice("filterExclude", nil, "expr-lang expressions; record is dropped if any evaluates true (repeatable)")
}

// setup loads configuration from file + env + flags, creates a logger, and sets
// up a signal-aware context. All subcommands share this initialisation sequence.
func setup(cmd *cobra.Command) (config.Config, *logrus.Logger, context.Context, context.CancelFunc, error) {
	return setupWithMode(cmd, "")
}

// setupWithMode is like setup but forces cfg.Mode to the given value (when
// non-empty) before validation, so subcommands such as backfill can ensure
// mode-specific validation runs even if the YAML file declares a different
// mode (e.g. the user keeps the default mode: daemon for normal operation
// and only flips to backfill via the CLI).
func setupWithMode(cmd *cobra.Command, mode string) (config.Config, *logrus.Logger, context.Context, context.CancelFunc, error) {
	cfg, err := config.Load(configFile, cmd.Flags())
	if err != nil {
		return config.Config{}, nil, nil, nil, err
	}
	if mode != "" {
		cfg.Mode = mode
	}

	if err := cfg.Validate(); err != nil {
		return config.Config{}, nil, nil, nil, err
	}

	logger := newLogger(cfg)

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)

	// Apply the MongoDB-backed remote override (no-op unless enabled). This
	// runs for every mode at startup; daemon additionally hot-reloads later.
	merged, err := remoteconfig.ApplyAtStartup(ctx, cfg, logger)
	if err != nil {
		cancel()
		return config.Config{}, nil, nil, nil, err
	}
	// Remote override may change the log level.
	logger = newLogger(merged)

	return merged, logger, ctx, cancel, nil
}

// newLogger creates a structured logrus logger from the config.
func newLogger(cfg config.Config) *logrus.Logger {
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	level, err := logrus.ParseLevel(strings.ToLower(cfg.LogLevel))
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)
	return logger
}
