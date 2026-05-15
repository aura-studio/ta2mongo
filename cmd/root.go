// Package cmd defines the CLI commands for ta2mongo.
package cmd

import (
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/ta2mongo/config"
)

var configFile string

// NewRoot creates and returns the root cobra command.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "ta2mongo",
		Short: "Tail ThinkingData log files and import them into MongoDB",
		RunE:  runDefault,
	}

	root.PersistentFlags().StringVar(&configFile, "config", "ta2mongo.yaml", "path to YAML config file")

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

// runDefault dispatches to the appropriate mode based on the YAML config `mode` field.
// This is used when no subcommand is specified on the CLI.
func runDefault(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}

	switch cfg.Mode {
	case config.ModeOnce:
		return runOnce(cmd, args)
	case config.ModeIngest:
		return runIngest(cmd, args)
	default:
		return runDaemon(cmd, args)
	}
}
