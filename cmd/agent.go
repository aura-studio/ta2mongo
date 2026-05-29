package cmd

import (
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/agent"
)

// NewAgent creates the agent subcommand.
func NewAgent() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run as a task worker: claim and execute tasks published to MongoDB",
		Long: `agent runs a long-lived worker that registers a heartbeat, claims tasks from
a shared MongoDB queue, and executes them (backfill / sql), renewing the lease
while working and reporting the result.

Tasks may be untargeted (any agent runs them) or targeted at a specific
instance. This process's identity comes from the TANGO_INSTANCE_ID environment
variable, which is required.`,
		RunE: runAgent,
	}

	addCommonFlags(cmd.Flags())
	return cmd
}

func runAgent(cmd *cobra.Command, _ []string) error {
	cfg, logger, ctx, cancel, err := setupWithMode(cmd, config.ModeAgent)
	if err != nil {
		return err
	}
	defer cancel()

	logger.WithFields(logrus.Fields{
		"instanceID": cfg.InstanceID,
		"mongo_uri":  maskURI(cfg.Mongo.URI),
	}).Info("agent: process starting")

	a, err := agent.New(ctx, cfg, logger)
	if err != nil {
		logger.WithError(err).Error("agent: initialize failed")
		return err
	}
	defer func() {
		if err := a.Shutdown(); err != nil {
			logger.WithError(err).Error("agent: shutdown error")
		}
	}()

	if err := a.EnsureIndexes(ctx); err != nil {
		logger.WithError(err).Error("agent: ensure indexes failed")
		return err
	}

	return a.Run(ctx)
}
