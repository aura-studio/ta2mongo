package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	ta2config "rocket-nano/aura-studio/ta2mongo/config"
	ta2runner "rocket-nano/aura-studio/ta2mongo/runner"
)

func main() {
	var configFile string

	root := &cobra.Command{
		Use:   "ta2mongo",
		Short: "Import ThinkingData logs into MongoDB (daemon only, config-file driven)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if configFile == "" {
				configFile = "ta2mongo.yaml"
			}

			v := viper.New()
			v.SetConfigFile(configFile)
			if err := v.ReadInConfig(); err != nil {
				return fmt.Errorf("read config %q: %w", configFile, err)
			}

			cfg, err := ta2config.LoadConfig(v)
			if err != nil {
				if errors.Is(err, viper.ConfigFileNotFoundError{}) {
					return err
				}
				return err
			}

			logger := logrus.New()
			logger.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
			level, err := logrus.ParseLevel(strings.ToLower(cfg.Log.Level))
			if err != nil {
				return fmt.Errorf("invalid log level %q: %w", cfg.Log.Level, err)
			}
			logger.SetLevel(level)

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM, syscall.SIGINT)
			defer cancel()

			runner, err := ta2runner.NewRunner(ctx, cfg, logger)
			if err != nil {
				return err
			}

			if err := runner.EnsureIndexes(ctx); err != nil {
				return err
			}

			return runner.RunDaemon(ctx)
		},
	}

	// Only allow selecting the config file location; no runtime tuning flags.
	root.Flags().StringVar(&configFile, "config", "ta2mongo.yaml", "2-level yaml config file path")

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
