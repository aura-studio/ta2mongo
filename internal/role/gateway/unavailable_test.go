package gateway

// Degraded unavailable mode: with dao.mongo.uri empty the gateway still starts
// and serves — /healthz stays 200 (liveness) while every API route answers
// 503 with the exact English body "uri is empty, service unavailable" and no
// database is ever touched.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// TestUnavailable_APIRoutesReturn503: the nil-engine Server's routes.
func TestUnavailable_APIRoutesReturn503(t *testing.T) {
	srv := &Server{} // nil engine = unavailable mode (what Role.Run constructs)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Liveness stays green so the orchestrator keeps the pod.
	hresp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 in unavailable mode", hresp.StatusCode)
	}

	const wantMsg = "uri is empty, service unavailable"
	for _, route := range []string{"/upload", "/ejson", "/sql", "/config"} {
		resp, err := http.Post(ts.URL+route, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("POST %s: %v", route, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", route, resp.StatusCode)
		}
		var payload struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("%s body %q is not the JSON error shape: %v", route, body, err)
			continue
		}
		if payload.Error != wantMsg {
			t.Errorf("%s error = %q, want %q", route, payload.Error, wantMsg)
		}
	}

	// Close must be nil-safe (no connection to release).
	if err := srv.Close(); err != nil {
		t.Errorf("Close() in unavailable mode = %v, want nil", err)
	}
}

// TestUnavailable_RoleRunStartsAndStops: the full Role.Run path with an empty
// URI binds, stays up, and shuts down cleanly on ctx cancel — no Mongo needed.
func TestUnavailable_RoleRunStartsAndStops(t *testing.T) {
	tree := cfgtree.New(map[string]any{
		"role": map[string]any{
			"mode":    "gateway",
			"gateway": map[string]any{"addr": "127.0.0.1:0"}, // random free port
		},
		// dao.mongo.uri intentionally absent.
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (Role{}).Run(ctx, tree) }()

	// Must stay up (not exit with an init error).
	select {
	case err := <-done:
		t.Fatalf("gateway exited immediately in unavailable mode (err=%v); want it to serve", err)
	case <-time.After(500 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unavailable gateway returned %v on shutdown, want nil", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("unavailable gateway did not exit within 15s of ctx cancel")
	}
}
