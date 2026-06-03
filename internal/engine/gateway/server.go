package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	sdk "rocket-nano/tools/tango/client"
	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/logging"
)

type Server struct {
	cc  config.ClientConfig
	cli *sdk.Client
}

func New(cc config.ClientConfig, cli *sdk.Client) *Server {
	return &Server{cc: cc, cli: cli}
}

func (s *Server) Run(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/ingest", s.handleIngest)
	mux.HandleFunc("/upload", s.handleUpload)

	httpSrv := &http.Server{Addr: addr, Handler: mux}
	errCh := make(chan error, 1)
	go func() {
		logging.WithField("addr", addr).Info("tango gateway: HTTP server listening")
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
