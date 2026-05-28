package cmd

import (
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/backfill"
)

// NewBackfill creates the backfill subcommand.
func NewBackfill() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Pull historical data from ThinkingData OpenAPI and import into MongoDB",
		Long: `backfill drives the ThinkingData async-SQL OpenAPI to pull a date range of
historical data and route each row through the same parse → filter → write
pipeline used by daemon/once.

The mode requires a backfill.* config block (apiBaseURL, token, projectID,
runID, partDateRange) and supports resume across restarts via a Mongo
_backfill_progress document keyed by runID.`,
		RunE: runBackfill,
	}

	addCommonFlags(cmd.Flags())
	return cmd
}

func runBackfill(cmd *cobra.Command, _ []string) error {
	cfg, logger, ctx, cancel, err := setupWithMode(cmd, config.ModeBackfill)
	if err != nil {
		return err
	}
	defer cancel()

	logger.WithFields(logrus.Fields{
		"runID":     cfg.Backfill.RunID,
		"projectID": cfg.Backfill.ProjectID,
		"table":     cfg.Backfill.Table,
		"mongo_uri": maskURI(cfg.MongoURI),
	}).Info("backfill: process starting")

	r, err := backfill.New(ctx, cfg, logger)
	if err != nil {
		logger.WithError(err).Error("backfill: initialize failed")
		return err
	}
	defer func() {
		if err := r.Shutdown(); err != nil {
			logger.WithError(err).Error("backfill: shutdown error")
		}
	}()

	logger.Info("backfill: ensuring MongoDB indexes")
	if err := r.EnsureIndexes(ctx); err != nil {
		logger.WithError(err).Error("backfill: ensure indexes failed")
		return err
	}

	return r.Run(ctx)
}
