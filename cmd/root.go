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
// Flag names mirror the YAML config keys exactly (dot-separated for nested fields).
// This allows viper to bind them directly without any name translation.
func addCommonFlags(flags *pflag.FlagSet) {
	// mongo
	flags.String("mongo.uri", "", "MongoDB connection URI (required)")

	// ta
	flags.StringSlice("ta.logPattern", nil, "regex patterns for matching log file paths (repeatable)")

	// tail
	flags.Int("tail.rescanSeconds", 0, "how often to rescan for new log files (seconds, default 30)")

	// batch
	flags.Int("batch.sizeMin", 0, "minimum batch size (default: sizeInitial/4)")
	flags.Int("batch.sizeInitial", 0, "initial/target batch size (default 1000)")
	flags.Int("batch.sizeMax", 0, "maximum batch size (default: sizeInitial*2)")
	flags.Int("batch.workers", 0, "number of parallel write workers (default 2)")
	flags.Int("batch.channelSize", 0, "per-worker channel buffer size (default 1000)")
	flags.Duration("batch.flushInterval", 0, "how often workers flush partial batches (default 1s)")

	// retry
	flags.Duration("retry.maxElapsedTime", 0, "max total retry time for bulk writes (default 10s)")

	// log
	flags.String("log.level", "", "log level: debug, info, warn, error (default info)")
}

// setup loads configuration from file + env + flags, creates a logger, and sets
// up a signal-aware context. All subcommands share this initialisation sequence.
func setup(cmd *cobra.Command) (config.Config, *logrus.Logger, context.Context, context.CancelFunc, error) {
	cfg, err := config.Load(configFile, cmd.Flags())
	if err != nil {
		return config.Config{}, nil, nil, nil, err
	}

	if err := cfg.Validate(); err != nil {
		return config.Config{}, nil, nil, nil, err
	}

	logger := newLogger(cfg)

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	return cfg, logger, ctx, cancel, nil
}

// newLogger creates a structured logrus logger from the config.
func newLogger(cfg config.Config) *logrus.Logger {
	logger := logrus.New()
	logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	level, err := logrus.ParseLevel(strings.ToLower(cfg.Log.Level))
	if err != nil {
		level = logrus.InfoLevel
	}
	logger.SetLevel(level)
	return logger
}
