package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/sirupsen/logrus"

	sdk "rocket-nano/tools/tango/client"
	"rocket-nano/tools/tango/config"
)

type Server struct {
	cc     config.ClientConfig
	cli    *sdk.Client
	logger *logrus.Logger
}

func New(cc config.ClientConfig, cli *sdk.Client, logger *logrus.Logger) *Server {
	return &Server{cc: cc, cli: cli, logger: logger}
}

func (s *Server) Run(ctx context.Context, addr string) error {
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
		s.logger.WithField("addr", addr).Info("tango gateway: HTTP server listening")
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

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
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
	res, err := s.cli.UploadFiles(r.Context(), sdk.UploadRequest{
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

func (s *Server) handleBackfill(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleSQL(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handlePublishReportSync(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handlePublishBackfill(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handlePublishSQL(w http.ResponseWriter, r *http.Request) {
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
