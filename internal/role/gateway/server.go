package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/role/api"
)

// Server is the gateway runtime: the embedded api engine plus the HTTP face. It
// is safe for concurrent use from multiple goroutines.
type Server struct {
	engine *api.Engine
}

// New builds a Server, connecting to MongoDB via the api engine. It is wired
// from the shared top-level module configs (dao, the process strategy config,
// the parser config carrying the reporting filter, and the cfgsync config for
// runtime filter hot-swap and config publishing) plus the gateway role config.
// cfgsyncCfg may be nil (cfgsync disabled). The caller must Close it.
func New(ctx context.Context, daoCfg *dao.Config, procCfg *process.Config, parserCfg *parser.Config, cfgsyncCfg *cfgsync.Config, cfg Config) (*Server, error) {
	cfg.ApplyDefaults()
	eng, err := api.New(ctx, daoCfg, procCfg, parserCfg, cfgsyncCfg)
	if err != nil {
		return nil, err
	}
	return &Server{engine: eng}, nil
}

// Close disconnects from MongoDB and releases all resources.
func (s *Server) Close() error { return s.engine.Close() }

// EnsureIndexes creates all required MongoDB indexes (idempotent).
func (s *Server) EnsureIndexes(ctx context.Context) error { return s.engine.EnsureIndexes(ctx) }

// Upload ingests lines with the configured process.mode (the engine entry
// point, exposed for programmatic/test use).
func (s *Server) Upload(ctx context.Context, lines []string) (api.Result, error) {
	return s.engine.Upload(ctx, lines)
}

// EJSON executes a Mongo Data API request (the engine entry point, exposed for
// programmatic/test use).
func (s *Server) EJSON(ctx context.Context, req *dao.EJSONRequest) (*dao.EJSONResponse, error) {
	return s.engine.EJSON(ctx, req)
}

// SQL executes a SQL statement (the engine entry point, exposed for
// programmatic/test use).
func (s *Server) SQL(ctx context.Context, query string) (*dao.SQLResult, error) {
	return s.engine.SQL(ctx, query)
}

// PublishConfig validates and publishes a runtime config document, returning the
// new monotonic version (the engine entry point, exposed for programmatic/test
// use).
func (s *Server) PublishConfig(ctx context.Context, doc bson.M) (int64, error) {
	return s.engine.PublishConfig(ctx, doc)
}

// Handler builds the HTTP handler exposing the gateway's routes: /healthz, the
// TA-ingest /upload, and the Mongo Data API /ejson. It is exported so the routes
// can be exercised directly (e.g. via httptest) without binding a socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/ejson", s.handleEJSON)
	mux.HandleFunc("/sql", s.handleSQL)
	mux.HandleFunc("/config", s.handleConfig)
	return mux
}

// Run starts the HTTP server and blocks until ctx is cancelled. It also launches
// the cfgsync Watcher (when enabled) so the gateway's live reporting filter is
// hot-swapped from the central config document while it serves.
func (s *Server) Run(ctx context.Context, addr string) error {
	s.engine.StartCfgsync(ctx)

	httpSrv := &http.Server{Addr: addr, Handler: s.Handler()}
	errCh := make(chan error, 1)
	go func() {
		defer logging.Recover("gateway http server")
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

// ejsonMarshaler is anything that encodes itself as Extended JSON — both
// *dao.EJSONResponse and *dao.SQLResult satisfy it.
type ejsonMarshaler interface{ MarshalEJSON() ([]byte, error) }

func writeEJSON(w http.ResponseWriter, code int, resp ejsonMarshaler) {
	body, err := resp.MarshalEJSON()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/ejson")
	w.WriteHeader(code)
	_, _ = w.Write(body)
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

// handleUpload ingests the request-body log lines through the configured
// process.mode. The body carries an array of lines and/or a single line; both
// are wrapped as one httpbody source.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
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

	res, err := s.engine.Upload(r.Context(), lines)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleEJSON executes a Mongo Data API request. The body is Extended JSON (or
// plain JSON, a subset) carrying the action plus its operands; the response is
// relaxed Extended JSON. It is the independent Mongo Data API path, separate from
// the TA-ingest /upload endpoint.
func (s *Server) handleEJSON(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, errors.New("POST required"))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req, err := dao.DecodeEJSONRequest(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp, err := s.engine.EJSON(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeEJSON(w, http.StatusOK, resp)
}

// handleSQL parses and executes a SQL statement (SQL Data API). The body is JSON
// {"sql":"..."}; the response is relaxed Extended JSON (SELECT rows carry BSON
// types). It is an independent path, separate from /upload and /ejson.
func (s *Server) handleSQL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SQL string `json:"sql"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if req.SQL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("sql is required"))
		return
	}
	res, err := s.engine.SQL(r.Context(), req.SQL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeEJSON(w, http.StatusOK, res)
}

// handleConfig publishes a runtime config document to the central collection
// (the write side of cfgsync; the daemon/gateway watchers pick it up). The body
// is the config content as JSON (e.g. {"filter":{"include":[...],"exclude":[...]}});
// cfgsync owns _id and version. On success the response is {"version": <new>}. A
// document carrying a subtree off the allowlist or a filter that does not compile
// is rejected with 400 before any write. It is independent of /upload, /ejson and
// /sql; /ingest is intentionally still not provided.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	var doc bson.M
	if !decodeBody(w, r, &doc) {
		return
	}
	version, err := s.engine.PublishConfig(r.Context(), doc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"version": version})
}
