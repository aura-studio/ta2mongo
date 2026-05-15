package cmd

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/once"
)

// NewOnce creates the once subcommand.
func NewOnce() *cobra.Command {
	return &cobra.Command{
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
		RunE: runOnce,
	}
}

func runOnce(cmd *cobra.Command, _ []string) error {
	cfg, logger, ctx, cancel, err := setup(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	return runOnceWithConfig(cmd, cfg, logger, ctx)
}

func runOnceWithConfig(_ *cobra.Command, cfg config.Config, logger *logrus.Logger, ctx context.Context) error {
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
