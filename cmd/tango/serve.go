package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/client"
	"rocket-nano/tools/tango/config"
)

// newServeCmd implements the HTTP/REST face: the five client functions exposed
// as JSON endpoints. The server holds one connected client for its lifetime.
func newServeCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP/REST server exposing the five client functions",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cc, cli, logger, err := loadClient(cmd)
			if err != nil {
				return err
			}
			defer cli.Close()
			if err := cli.EnsureIndexes(cmd.Context()); err != nil {
				return err
			}
			if addr == "" {
				addr = cc.Server.Addr
			}
			srv := &server{cc: cc, cli: cli, logger: logger}
			return srv.run(cmd.Context(), addr)
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "", "HTTP listen address; overrides server.addr")
	return cmd
}

type server struct {
	cc     config.ClientConfig
	cli    *client.Client
	logger *logrus.Logger
}

func (s *server) run(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/ingest", s.handleIngest)
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/backfill", s.handleBackfill)
	mux.HandleFunc("/sql", s.handleSQL)
	mux.HandleFunc("/publish/report-sync", s.handlePublishReportSync)
	mux.HandleFunc("/publish/backfill", s.handlePublishBackfill)
	mux.HandleFunc("/publish/sql", s.handlePublishSQL)

	httpSrv := &http.Server{Addr: addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		s.logger.WithField("addr", addr).Info("tango: HTTP server listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// ---- request/response helpers ------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return false
	}
	if r.Body == nil {
		return true
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

// ---- handlers ---------------------------------------------------------------

func (s *server) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Line  string   `json:"line"`
		Lines []string `json:"lines"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	lines := req.Lines
	if req.Line != "" {
		lines = append(lines, req.Line)
	}
	if err := s.cli.IngestBatch(r.Context(), lines); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ingested": len(lines)})
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Patterns  []string `json:"patterns"`
		BatchSize int      `json:"batchSize"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Patterns) == 0 {
		req.Patterns = s.cc.FileUpload.LogPattern
	}
	if req.BatchSize == 0 {
		req.BatchSize = s.cc.FileUpload.Pipeline.BatchSize
	}
	res, err := s.cli.UploadFiles(r.Context(), client.UploadRequest{
		Patterns:             req.Patterns,
		BatchSize:            req.BatchSize,
		CheckpointCollection: s.cc.FileUpload.CheckpointCollection,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) handleBackfill(w http.ResponseWriter, r *http.Request) {
	if !decodeBody(w, r, &struct{}{}) {
		return
	}
	res, err := s.cli.RunBackfill(r.Context(), s.cc.BackfillRuntime())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) handleSQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SQL string `json:"sql"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	rows, err := s.cli.ExecuteSQL(r.Context(), s.cc.SQLRuntime(), req.SQL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *server) handlePublishReportSync(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Include []string `json:"include"`
		Exclude []string `json:"exclude"`
		Target  string   `json:"target"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	id, err := s.cli.PublishReportSync(r.Context(), req.Include, req.Exclude, req.Target)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskID": id})
}

func (s *server) handlePublishBackfill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Payload map[string]any `json:"payload"`
		Target  string         `json:"target"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	id, err := s.cli.PublishBackfillTask(r.Context(), req.Payload, req.Target)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskID": id})
}

func (s *server) handlePublishSQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SQL    string `json:"sql"`
		Table  string `json:"table"`
		Target string `json:"target"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Table == "" {
		req.Table = s.cc.BackfillFilter.Table
	}
	id, err := s.cli.PublishSQLTask(r.Context(), req.SQL, req.Table, req.Target)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"taskID": id})
}
