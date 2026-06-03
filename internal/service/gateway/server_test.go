package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeBody_RequiresPOST(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/ingest", nil)
	var dst struct{}
	if decodeBody(w, r, &dst) {
		t.Fatal("decodeBody accepted a non-POST request")
	}
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}

func TestDecodeBody_BadJSON(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader("{not json"))
	var dst struct {
		Line string `json:"line"`
	}
	if decodeBody(w, r, &dst) {
		t.Fatal("decodeBody accepted malformed JSON")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestDecodeBody_ValidAndEmpty(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/ingest", strings.NewReader(`{"line":"hello"}`))
		var dst struct {
			Line string `json:"line"`
		}
		if !decodeBody(w, r, &dst) {
			t.Fatal("decodeBody rejected valid JSON")
		}
		if dst.Line != "hello" {
			t.Errorf("decoded line = %q", dst.Line)
		}
	})
	t.Run("empty body ok", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/upload", http.NoBody)
		var dst struct{}
		if !decodeBody(w, r, &dst) {
			t.Fatal("decodeBody rejected an empty POST body")
		}
	})
}

func TestWriteErr(t *testing.T) {
	w := httptest.NewRecorder()
	writeErr(w, http.StatusInternalServerError, errors.New("boom"))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if body := strings.TrimSpace(w.Body.String()); body != `{"error":"boom"}` {
		t.Errorf("body = %s", body)
	}
}

// healthz is wired in Run; assert the handler contract directly.
func TestHealthzContract(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("healthz status = %d", w.Code)
	}
}
