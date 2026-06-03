package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role/api"
)

// Server is the gateway runtime: the embedded api engine plus the HTTP face. It
// is safe for concurrent use from multiple goroutines.
type Server struct {
	cfg    Config
	engine *api.Client
}

// New builds a Server from the shared dao config and the gateway role config,
// connecting to MongoDB via the api engine. The caller must Close it.
func New(ctx context.Context, daoCfg *dao.Config, cfg Config) (*Server, error) {
	cfg.ApplyDefaults()
	eng, err := api.New(ctx, daoCfg, cfg.Upload.ProcessConfig(), cfg.Upload.Filter)
	if err != nil {
		return nil, err
	}
	return &Server{cfg: cfg, engine: eng}, nil
}

// Close disconnects from MongoDB and releases all resources.
func (s *Server) Close() error { return s.engine.Close() }

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (s *Server) EnsureIndexes(ctx context.Context) error { return s.engine.EnsureIndexes(ctx) }

// Upload ingests lines with the given mode (the engine entry point, exposed for
// programmatic/test use).
func (s *Server) Upload(ctx context.Context, mode process.Mode, lines []string) (api.Result, error) {
	return s.engine.Upload(ctx, mode, lines)
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
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

// handleUpload ingests the request-body log lines through one of the three
// upload strategies. The body carries an array of lines (and/or a single line)
// plus an optional "mode" (single | batch | pipeline); an empty mode falls back
// to the configured default. Both the array and the single line are wrapped as
// one httpbody source.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Mode  string   `json:"mode"`
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

	modeStr := req.Mode
	if modeStr == "" {
		modeStr = s.cfg.Upload.DefaultMode
	}
	mode, err := process.ParseMode(modeStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	res, err := s.engine.Upload(r.Context(), mode, lines)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
