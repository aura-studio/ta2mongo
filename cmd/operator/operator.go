// Package operator implements the `tango operator` command tree: one-shot
// ingest / upload / backfill / sql / publish operations aimed at humans,
// scripts, and CI/CD. It holds no long-running lifecycle.
package operator

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	sdk "rocket-nano/tools/tango/client"
	"rocket-nano/tools/tango/cmd/shared"
)

// NewCommand builds the `tango operator` parent command.
func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "operator",
		Short: "Operator CLI: one-shot ingest, upload, backfill, sql, and task publishing",
	}
	addConnectionFlags(cmd)
	cmd.AddCommand(Subcommands()...)
	return cmd
}

// addConnectionFlags registers the shared mongo/logging persistent overrides.
func addConnectionFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().String("mongo.uri", "", "MongoDB connection URI (config key mongo.uri)")
	cmd.PersistentFlags().String("logging.level", "", "log level: debug, info, warn, error (config key logging.level)")
}

// Subcommands returns the one-shot operator subcommands. They are reused by the
// legacy `tango client` wrapper.
func Subcommands() []*cobra.Command {
	return []*cobra.Command{
		newIngestCmd(), newUploadCmd(), newBackfillCmd(), newSQLCmd(), newPublishCmd(),
	}
}

func newIngestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ingest [json-line ...]",
		Short: "String single upload (no retransmission): ingest JSON lines from args/stdin",
		RunE: func(cmd *cobra.Command, args []string) error {
			cc, logger, err := shared.LoadClientConfig(cmd)
			if err != nil {
				return err
			}
			cli, err := shared.BuildClient(cmd, cc, logger,
				sdk.WithBatchSize(cc.StringUpload.BatchSize),
				sdk.WithFilter(cc.StringUpload.Filter.Include, cc.StringUpload.Filter.Exclude))
			if err != nil {
				return err
			}
			defer cli.Close()
			ctx := cmd.Context()
			if err := cli.EnsureIndexes(ctx); err != nil {
				return err
			}
			lines := append([]string{}, args...)
			if info, _ := os.Stdin.Stat(); info != nil && (info.Mode()&os.ModeCharDevice) == 0 {
				sc := bufio.NewScanner(os.Stdin)
				sc.Buffer(make([]byte, 0, 64*1024), cc.FileUpload.MaxLineBytes)
				for sc.Scan() {
					if t := strings.TrimSpace(sc.Text()); t != "" {
						lines = append(lines, t)
					}
				}
				if err := sc.Err(); err != nil {
					return err
				}
			}
			var failed int
			for _, l := range lines {
				if err := cli.Ingest(ctx, l); err != nil {
					cmd.PrintErrln("ingest error:", err)
					failed++
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d line(s) failed", failed)
			}
			fmt.Printf("ingested %d line(s)\n", len(lines))
			return nil
		},
	}
}

func newUploadCmd() *cobra.Command {
	var patterns []string
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "File single upload (with resume/retransmission)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cc, logger, err := shared.LoadClientConfig(cmd)
			if err != nil {
				return err
			}
			cli, err := shared.BuildClient(cmd, cc, logger,
				sdk.WithFilter(cc.FileUpload.Filter.Include, cc.FileUpload.Filter.Exclude))
			if err != nil {
				return err
			}
			defer cli.Close()
			if len(patterns) == 0 {
				patterns = cc.FileUpload.LogPattern
			}
			if err := cli.EnsureIndexes(cmd.Context()); err != nil {
				return err
			}
			res, err := cli.UploadFiles(cmd.Context(), sdk.UploadRequest{
				Patterns:             patterns,
				BatchSize:            cc.FileUpload.Pipeline.BatchSize,
				CheckpointCollection: cc.FileUpload.CheckpointCollection,
			})
			if err != nil {
				return err
			}
			return shared.PrintJSON(res)
		},
	}
	cmd.Flags().StringArrayVar(&patterns, "logPattern", nil, "file regex pattern(s); overrides fileUpload.logPattern")
	return cmd
}

func newBackfillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "backfill",
		Short: "Backfill execution: pull historical data from the TA OpenAPI",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cc, cli, _, err := shared.LoadClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()
			res, err := cli.RunBackfill(cmd.Context(), cc.BackfillRuntime())
			if err != nil {
				return err
			}
			return shared.PrintJSON(res)
		},
	}
}

func newSQLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sql <statement>",
		Short: "SQL execution: run an ad-hoc TA SQL statement and import the rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cc, cli, _, err := shared.LoadClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()
			rows, err := cli.ExecuteSQL(cmd.Context(), cc.SQLRuntime(), args[0])
			if err != nil {
				return err
			}
			fmt.Printf("imported %d row(s)\n", rows)
			return nil
		},
	}
}

func newPublishCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "publish", Short: "Publish tasks/filters to the worker task queue"}
	cmd.AddCommand(newPublishReportSyncCmd(), newPublishBackfillCmd(), newPublishSQLCmd())
	return cmd
}

func newPublishReportSyncCmd() *cobra.Command {
	var include, exclude []string
	var target string
	cmd := &cobra.Command{
		Use:   "report-sync",
		Short: "Publish a report-sync task: push a new reporting filter to report services",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, cli, _, err := shared.LoadClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()
			id, err := cli.PublishReportSync(cmd.Context(), include, exclude, target)
			if err != nil {
				return err
			}
			fmt.Println("published report-sync task:", id)
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&include, "include", nil, "filter include expression(s)")
	cmd.Flags().StringArrayVar(&exclude, "exclude", nil, "filter exclude expression(s)")
	cmd.Flags().StringVar(&target, "target", "", "target instance id (empty = any)")
	return cmd
}

func newPublishBackfillCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "backfill",
		Short: "Publish a backfill task using the config's backfill + backfillFilter",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cc, cli, _, err := shared.LoadClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()
			payload := map[string]any{
				"apiBaseURL": cc.Backfill.APIBaseURL,
				"token":      cc.Backfill.Token,
				"projectID":  cc.Backfill.ProjectID,
				"backfillFilter": map[string]any{
					"table":   cc.BackfillFilter.Table,
					"events":  cc.BackfillFilter.Events,
					"include": cc.BackfillFilter.Include,
					"exclude": cc.BackfillFilter.Exclude,
				},
			}
			id, err := cli.PublishBackfillTask(cmd.Context(), payload, target)
			if err != nil {
				return err
			}
			fmt.Println("published backfill task:", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "target instance id (empty = any)")
	return cmd
}

func newPublishSQLCmd() *cobra.Command {
	var target string
	cmd := &cobra.Command{
		Use:   "sql <statement>",
		Short: "Publish an ad-hoc SQL task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cc, cli, _, err := shared.LoadClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()
			id, err := cli.PublishSQLTask(cmd.Context(), args[0], cc.BackfillFilter.Table, target)
			if err != nil {
				return err
			}
			fmt.Println("published sql task:", id)
			return nil
		},
	}
	cmd.Flags().StringVar(&target, "target", "", "target instance id (empty = any)")
	return cmd
}
