package cmd

import (
	"context"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/daemon"
)

// NewDaemon creates the daemon subcommand.
func NewDaemon() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run as a daemon: tail log files and continuously import into MongoDB",
		RunE:  runDaemon,
	}
}

func runDaemon(cmd *cobra.Command, _ []string) error {
	cfg, logger, ctx, cancel, err := setup(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	return runDaemonWithConfig(cfg, logger, ctx)
}

func runDaemonWithConfig(cfg config.Config, logger *logrus.Logger, ctx context.Context) error {
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
