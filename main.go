package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/daemon"
	"rocket-nano/tools/ta2mongo/ingest"
	"rocket-nano/tools/ta2mongo/once"
)

func main() {
	var configFile string

	root := &cobra.Command{
		Use:   "ta2mongo",
		Short: "Tail ThinkingData log files and import them into MongoDB",
	}

	// Persistent flag shared by all subcommands.
	root.PersistentFlags().StringVar(&configFile, "config", "ta2mongo.yaml", "path to YAML config file")

	// --- daemon subcommand ---
	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run as a daemon: tail log files and continuously import into MongoDB",
		RunE:  runDaemon(&configFile),
	}

	// --- once subcommand ---
	onceCmd := &cobra.Command{
		Use:   "once",
		Short: "One-shot processing: read all matched files from beginning, process, then exit",
		Long: `Process all existing ThinkingData log files matching ta.logPattern from the
beginning (not incremental). After all lines are processed, print a detailed
summary of statistics (lines processed, errors, retries, throughput) and exit.

This mode is ideal for:
  - Batch data migration or re-import
  - Data recovery after failures
  - CI/CD pipelines where you want a clear pass/fail result
  - One-time historical data backfill

Unlike daemon mode, once mode:
  - Reads files from the BEGINNING (not just new lines)
  - Does NOT follow or re-open files
  - Exits when all files are fully processed
  - Provides detailed statistics summary at the end

Examples:
  # Process all matched files once:
  ta2mongo once --config ta2mongo.yaml

  # Can also be configured in YAML with mode: once`,
		RunE: runOnce(&configFile),
	}

	// --- ingest subcommand ---
	ingestCmd := &cobra.Command{
		Use:   "ingest [json-line ...]",
		Short: "Ingest JSON log lines synchronously (blocking)",
		Long: `Process ThinkingData JSON log lines one at a time with synchronous MongoDB writes.

Lines can be provided as positional arguments or piped via stdin (one line per line).
Each line is parsed, identity-resolved, and written to MongoDB before the next
line is processed. Exits with a non-zero status if any line fails.

Examples:
  # Single line as argument:
  ta2mongo ingest --config ta2mongo.yaml '{"#type":"track","#event_name":"login",...}'

  # Multiple lines from stdin:
  cat events.jsonl | ta2mongo ingest --config ta2mongo.yaml

  # Mix: arguments processed first, then stdin if piped:
  echo '{"#type":"track",...}' | ta2mongo ingest --config ta2mongo.yaml`,
		RunE: runIngest(&configFile),
	}

	root.AddCommand(daemonCmd, onceCmd, ingestCmd)

	// Default behavior: respect the mode field in YAML config.
	// If no subcommand is specified, load config and dispatch based on mode field.
	root.RunE = runDefault(&configFile)

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

// runDaemon returns the cobra RunE handler for daemon mode.
func runDaemon(configFile *string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(*configFile)
		if err != nil {
			return err
		}

		logger := newLogger(cfg)

		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		d, err := daemon.New(ctx, cfg, logger)
		if err != nil {
			return err
		}
		defer d.Shutdown()

		if err := d.EnsureIndexes(ctx); err != nil {
			return err
		}

		return d.Run(ctx)
	}
}

// runOnce returns the cobra RunE handler for one-shot mode.
func runOnce(configFile *string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		cfg, err := config.Load(*configFile)
		if err != nil {
			return err
		}

		logger := newLogger(cfg)

		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		r, err := once.New(ctx, cfg, logger)
		if err != nil {
			return err
		}
		defer r.Close()

		if err := r.EnsureIndexes(ctx); err != nil {
			return err
		}

		stats, err := r.Run(ctx)
		if err != nil {
			return err
		}

		if stats.HasErrors() {
			return fmt.Errorf("once: completed with errors (parse=%d, identity=%d, write=%d, retries=%d)",
				stats.ParseErrors.Load(), stats.IdentityErrors.Load(), stats.WriteErrors.Load(), stats.Retries)
		}
		return nil
	}
}

// runDefault dispatches to the appropriate mode based on the YAML config `mode` field.
// This is used when no subcommand is specified on the CLI.
func runDefault(configFile *string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(*configFile)
		if err != nil {
			return err
		}

		switch cfg.Mode {
		case config.ModeOnce:
			return runOnce(configFile)(cmd, args)
		case config.ModeIngest:
			return runIngest(configFile)(cmd, args)
		default:
			// Default to daemon mode (backward compatible).
			return runDaemon(configFile)(cmd, args)
		}
	}
}

// runIngest returns the cobra RunE handler for synchronous ingest mode.
func runIngest(configFile *string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(*configFile)
		if err != nil {
			return err
		}

		logger := newLogger(cfg)

		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		ig, err := ingest.New(ctx, cfg, logger)
		if err != nil {
			return err
		}
		defer ig.Close()

		if err := ig.EnsureIndexes(ctx); err != nil {
			return err
		}

		var failed int

		// Process positional arguments first.
		for _, line := range args {
			if err := ig.Ingest(ctx, line); err != nil {
				logger.WithError(err).Error("ingest failed")
				failed++
			}
		}

		// Then read from stdin if it's a pipe (not a terminal).
		if info, _ := os.Stdin.Stat(); info != nil && (info.Mode()&os.ModeCharDevice) == 0 {
			scanner := bufio.NewScanner(os.Stdin)
			// Support lines up to 10 MB (TA payloads can be large).
			scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.TrimSpace(line) == "" {
					continue
				}
				if err := ig.Ingest(ctx, line); err != nil {
					logger.WithError(err).Error("ingest failed")
					failed++
				}
			}
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("reading stdin: %w", err)
			}
		}

		if failed > 0 {
			return fmt.Errorf("%d line(s) failed to ingest", failed)
		}
		return nil
	}
}
