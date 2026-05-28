package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "test-token", &http.Client{Timeout: 5 * time.Second})
	return srv, c
}

func writeEnvelope(t *testing.T, w http.ResponseWriter, data any, code int, msg string) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	env := envelope{ReturnCode: code, ReturnMessage: msg, Data: raw}
	body, _ := json.Marshal(env)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func TestSubmitSQL_Success(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/submit-sql" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("token") != "test-token" {
			t.Errorf("missing token")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if got := r.PostForm.Get("sql"); !strings.Contains(got, "SELECT") {
			t.Errorf("form sql = %q", got)
		}
		writeEnvelope(t, w, SubmitResult{TaskID: "abc-123"}, 0, "success")
	})

	id, err := c.SubmitSQL(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if id != "abc-123" {
		t.Errorf("taskID = %q, want abc-123", id)
	}
}

func TestSubmitSQL_APIError(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(t, w, nil, -1008, "参数(token)为空")
	})
	_, err := c.SubmitSQL(context.Background(), "SELECT 1")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want APIError, got %T", err)
	}
	if apiErr.Code != -1008 {
		t.Errorf("code = %d", apiErr.Code)
	}
}

func TestTaskInfo_Finished(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/open/sql-task-info" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("taskId") != "abc" {
			t.Errorf("taskId missing")
		}
		info := TaskInfoResult{TaskID: "abc", Status: StatusFinished, Progress: 100}
		info.ResultStat.Headers = []string{"#type", "level"}
		info.ResultStat.PageCount = 3
		info.ResultStat.PageSize = 10000
		info.ResultStat.RowCount = 25000
		writeEnvelope(t, w, info, 0, "ok")
	})
	got, err := c.TaskInfo(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusFinished || got.ResultStat.PageCount != 3 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestTaskInfo_Expired(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(t, w, nil, -1, "task not found")
	})
	_, err := c.TaskInfo(context.Background(), "abc")
	if !errors.Is(err, ErrTaskExpired) {
		t.Fatalf("want ErrTaskExpired, got %v", err)
	}
}

func TestResultPage_Success(t *testing.T) {
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageId") != "2" {
			t.Errorf("pageId = %q", r.URL.Query().Get("pageId"))
		}
		page := ResultPageResult{
			TaskID:    "abc",
			Headers:   []string{"#type", "level"},
			PageCount: 3,
			PageSize:  10000,
			PageID:    2,
			RowCount:  25000,
			Rows: [][]interface{}{
				{"track", float64(5)},
				{"user_set", float64(0)},
			},
		}
		writeEnvelope(t, w, page, 0, "ok")
	})
	got, err := c.ResultPage(context.Background(), "abc", 2, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rows) != 2 || got.PageID != 2 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestDo_RetriesOn500ThenSucceeds(t *testing.T) {
	var calls int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("boom"))
			return
		}
		writeEnvelope(t, w, SubmitResult{TaskID: "ok"}, 0, "")
	})
	id, err := c.SubmitSQL(context.Background(), "SELECT 1")
	if err != nil {
		t.Fatal(err)
	}
	if id != "ok" {
		t.Errorf("id = %q", id)
	}
	if got := atomic.LoadInt32(&calls); got < 2 {
		t.Errorf("expected at least 2 calls (one 500 then success), got %d", got)
	}
}

func TestDo_NoRetryOn400(t *testing.T) {
	var calls int32
	_, c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad")
	})
	_, err := c.SubmitSQL(context.Background(), "SELECT 1")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", got)
	}
}
