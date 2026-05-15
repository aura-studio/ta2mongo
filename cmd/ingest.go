package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/ta2mongo/config"
	"rocket-nano/tools/ta2mongo/ingest"
)

// NewIngest creates the ingest subcommand.
func NewIngest() *cobra.Command {
	return &cobra.Command{
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
		RunE: runIngest,
	}
}

func runIngest(cmd *cobra.Command, args []string) error {
	cfg, logger, ctx, cancel, err := setup(cmd)
	if err != nil {
		return err
	}
	defer cancel()

	return runIngestWithConfig(cmd, args, cfg, logger, ctx)
}

func runIngestWithConfig(_ *cobra.Command, args []string, cfg config.Config, logger *logrus.Logger, ctx context.Context) error {
	ig, err := ingest.New(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer ig.Close()

	if err := ig.EnsureIndexes(ctx); err != nil {
		return err
	}

	var failed int

	for _, line := range args {
		if err := ig.Ingest(ctx, line); err != nil {
			logger.WithError(err).Error("ingest failed")
			failed++
		}
	}

	if info, _ := os.Stdin.Stat(); info != nil && (info.Mode()&os.ModeCharDevice) == 0 {
		scanner := bufio.NewScanner(os.Stdin)
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
