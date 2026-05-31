// Command tango is the tango client: a single binary exposing the five client
// functions — string upload, file upload (with resume), backfill, ad-hoc SQL,
// and task publishing — across three faces: one-shot CLI subcommands, an
// HTTP/REST server (`tango serve`), and (via the importable client package) an
// embeddable Go library.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/client"
	"rocket-nano/tools/tango/config"
)

var configFile string

func main() {
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "tango",
		Short: "Tango client: upload, backfill, sql, and task publishing",
	}
	root.PersistentFlags().StringVar(&configFile, "config", "client.yaml",
		"path to client config file (.yaml/.yml/.json; skipped if absent)")
	root.PersistentFlags().String("mongoURI", "", "MongoDB connection URI (maps to mongo.uri)")
	root.PersistentFlags().String("logLevel", "", "log level: debug, info, warn, error")

	root.AddCommand(newIngestCmd(), newUploadCmd(), newBackfillCmd(), newSQLCmd(), newPublishCmd(), newServeCmd())
	return root
}

// loadClientConfig loads the client config (file + env + flags).
func loadClientConfig(cmd *cobra.Command) (config.ClientConfig, *logrus.Logger, error) {
	cc, err := config.LoadClient(configFile, cmd.Flags())
	if err != nil {
		return config.ClientConfig{}, nil, err
	}
	return cc, newLogger(cc.Logging.Level), nil
}

// buildClient constructs a connected client from the config, layering the given
// functional options on top of the config-derived connection settings.
func buildClient(cmd *cobra.Command, cc config.ClientConfig, logger *logrus.Logger, extra ...client.Option) (*client.Client, error) {
	opts := append([]client.Option{
		client.WithURI(cc.Mongo.URI),
		client.WithMaxElapsedTime(cc.Mongo.MaxElapsedTime),
		client.WithLogger(logger),
		client.WithTaskQueue(cc.Publish.TasksCollection, cc.Publish.InstancesCollection, cc.Publish.InstanceTTL),
	}, extra...)
	return client.New(cmd.Context(), opts...)
}

// loadClient is the common path for commands that need a plain client.
func loadClient(cmd *cobra.Command, extra ...client.Option) (config.ClientConfig, *client.Client, *logrus.Logger, error) {
	cc, logger, err := loadClientConfig(cmd)
	if err != nil {
		return config.ClientConfig{}, nil, nil, err
	}
	cli, err := buildClient(cmd, cc, logger, extra...)
	if err != nil {
		return cc, nil, nil, err
	}
	return cc, cli, logger, nil
}

func newIngestCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ingest [json-line ...]",
		Short: "String single upload (no retransmission): ingest JSON lines from args/stdin",
		RunE: func(cmd *cobra.Command, args []string) error {
			cc, logger, err := loadClientConfig(cmd)
			if err != nil {
				return err
			}
			cli, err := buildClient(cmd, cc, logger,
				client.WithBatchSize(cc.StringUpload.BatchSize),
				client.WithFilter(cc.StringUpload.Filter.Include, cc.StringUpload.Filter.Exclude))
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
			cc, logger, err := loadClientConfig(cmd)
			if err != nil {
				return err
			}
			cli, err := buildClient(cmd, cc, logger,
				client.WithFilter(cc.FileUpload.Filter.Include, cc.FileUpload.Filter.Exclude))
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
			res, err := cli.UploadFiles(cmd.Context(), client.UploadRequest{
				Patterns:             patterns,
				BatchSize:            cc.FileUpload.Pipeline.BatchSize,
				CheckpointCollection: cc.FileUpload.CheckpointCollection,
			})
			if err != nil {
				return err
			}
			return printJSON(res)
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
			cc, cli, _, err := loadClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()
			res, err := cli.RunBackfill(cmd.Context(), cc.BackfillRuntime())
			if err != nil {
				return err
			}
			return printJSON(res)
		},
	}
}

func newSQLCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sql <statement>",
		Short: "SQL execution: run an ad-hoc TA SQL statement and import the rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cc, cli, _, err := loadClient(cmd)
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
	cmd := &cobra.Command{Use: "publish", Short: "Publish tasks/filters to the agent task queue"}
	cmd.AddCommand(newPublishReportSyncCmd(), newPublishBackfillCmd(), newPublishSQLCmd())
	return cmd
}

func newPublishReportSyncCmd() *cobra.Command {
	var include, exclude []string
	var target string
	cmd := &cobra.Command{
		Use:   "report-sync",
		Short: "Publish a report-sync task: push a new reporting filter to daemons",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, cli, _, err := loadClient(cmd)
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
			cc, cli, _, err := loadClient(cmd)
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
			cc, cli, _, err := loadClient(cmd)
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

func newLogger(level string) *logrus.Logger {
	l := logrus.New()
	l.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	lvl, err := logrus.ParseLevel(strings.ToLower(level))
	if err != nil {
		lvl = logrus.InfoLevel
	}
	l.SetLevel(lvl)
	return l
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}
