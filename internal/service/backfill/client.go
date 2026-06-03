// Package backfill orchestrates pulling historical data from ThinkingData's
// OpenAPI and routing it through the regular ingestion pipeline.
//
// The package centres on three components:
//
//   - Client: a thin HTTP client for the three async SQL endpoints
//     (/open/submit-sql, /open/sql-task-info, /open/sql-result-page) plus
//     /open/cancel-sql-task. The client deals with TA's envelope shape
//     ({"return_code", "return_message", "data": ...}) and the
//     token-as-query-param auth scheme.
//   - Checkpoint: a Mongo-backed progress store keyed by RunID that
//     remembers which days finished and where pagination is inside the
//     in-flight day.
//   - Runner: a per-day loop that submits → polls → paginates, feeds rows
//     into the shared lineCh consumed by pipeline.RunWorkers.
package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// Task lifecycle states returned by /open/sql-task-info.
const (
	StatusRunning  = "RUNNING"
	StatusFinished = "FINISHED"
	StatusFailed   = "FAILED"
)

// envelope captures TA's standard response wrapper:
//
//	{"return_code": 0, "return_message": "...", "data": {...}}
//
// A non-zero return_code is treated as an API error.
type envelope struct {
	ReturnCode    int             `json:"return_code"`
	ReturnMessage string          `json:"return_message"`
	Data          json.RawMessage `json:"data"`
}

// APIError is returned when the TA API responds with a non-zero return_code.
type APIError struct {
	Code    int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("ta openapi: return_code=%d return_message=%q", e.Code, e.Message)
}

// ErrTaskExpired is returned by TaskInfo / ResultPage when the task is gone
// (TTL'd or otherwise no longer addressable on the server). Callers should
// re-submit the SQL and reset pagination state on this error.
var ErrTaskExpired = errors.New("ta openapi: task expired or not found")

// Client is the ThinkingData OpenAPI client used by the backfill runner.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient constructs a Client with sensible HTTP defaults. The baseURL must
// have no trailing slash.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    httpClient,
	}
}

// SubmitResult is the unmarshalled "data" body of /open/submit-sql.
type SubmitResult struct {
	TaskID string `json:"taskId"`
}

// SubmitSQL submits an async SQL query and returns the assigned task ID.
//
// pageSize MUST be set here, not on the later sql-result-page call: the TA
// OpenAPI computes server-side pagination at submit time and defaults to "no
// pagination" when pageSize is omitted, which would force the entire result
// set into a single result-page response (pageCount=1). A non-zero pageSize
// yields pageCount = ceil(rowCount/pageSize) so each page can be fetched and
// checkpointed independently. pageSize < 1000 is clamped to the API minimum.
func (c *Client) SubmitSQL(ctx context.Context, sql string, pageSize int) (string, error) {
	form := url.Values{}
	form.Set("sql", sql)
	if pageSize > 0 {
		if pageSize < 1000 {
			pageSize = 1000
		}
		form.Set("pageSize", strconv.Itoa(pageSize))
	}

	var out SubmitResult
	if err := c.postForm(ctx, "/open/submit-sql", form, &out); err != nil {
		return "", err
	}
	if out.TaskID == "" {
		return "", fmt.Errorf("submit-sql: empty taskId in response")
	}
	return out.TaskID, nil
}

// TaskInfoResult mirrors the "data" body of /open/sql-task-info.
type TaskInfoResult struct {
	TaskID     string `json:"taskId"`
	Status     string `json:"status"`
	Progress   int    `json:"progress"`
	ResultStat struct {
		Headers   []string `json:"headers"`
		RowCount  int64    `json:"rowCount"`
		PageCount int      `json:"pageCount"`
		PageSize  int      `json:"pageSize"`
	} `json:"resultStat"`
}

// TaskInfo polls the status of an async SQL task.
func (c *Client) TaskInfo(ctx context.Context, taskID string) (*TaskInfoResult, error) {
	q := url.Values{}
	q.Set("taskId", taskID)

	var out TaskInfoResult
	if err := c.get(ctx, "/open/sql-task-info", q, &out); err != nil {
		if isExpired(err) {
			return nil, ErrTaskExpired
		}
		return nil, err
	}
	return &out, nil
}

// ResultPageResult mirrors the "data" body of /open/sql-result-page.
type ResultPageResult struct {
	TaskID    string          `json:"taskId"`
	Headers   []string        `json:"headers"`
	PageCount int             `json:"pageCount"`
	PageSize  int             `json:"pageSize"`
	PageID    int             `json:"pageId"`
	RowCount  int64           `json:"rowCount"`
	Rows      [][]interface{} `json:"rows"`
}

// ResultPage fetches a single page of results for an async SQL task and
// returns the rows in a single allocation. Suitable for small pages; for
// larger result sets prefer StreamResultPage to avoid buffering the whole
// response in memory.
func (c *Client) ResultPage(ctx context.Context, taskID string, pageID, pageSize int) (*ResultPageResult, error) {
	var rows [][]interface{}
	err := c.StreamResultPage(ctx, taskID, pageID, pageSize, func(row []interface{}) error {
		rows = append(rows, row)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ResultPageResult{Rows: rows, PageID: pageID}, nil
}

// StreamResultPage drives the TA result-page endpoint as an NDJSON stream and
// calls onRow for each row as it is decoded. Returning a non-nil error from
// onRow aborts the iteration and surfaces the error to the caller.
//
// The TA result-page endpoint returns pure NDJSON rows (no envelope), so
// headers + page metadata must be sourced separately from TaskInfo. On
// envelope-shaped error responses (HTTP 200 with non-zero return_code) we
// surface an APIError, mapping "task not found / expired" variants to
// ErrTaskExpired.
func (c *Client) StreamResultPage(ctx context.Context, taskID string, pageID, pageSize int,
	onRow func(row []interface{}) error) error {
	q := url.Values{}
	q.Set("taskId", taskID)
	q.Set("pageId", strconv.Itoa(pageID))
	if pageSize > 0 {
		q.Set("pageSize", strconv.Itoa(pageSize))
	}
	q.Set("format", "json")

	u := c.baseURL + "/open/sql-result-page?" + c.authQuery(q).Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}

	return streamResultPageBody(resp.Body, onRow)
}

// CancelTask asks TA to cancel an in-flight SQL task. Errors are surfaced but
// callers typically log-and-continue.
func (c *Client) CancelTask(ctx context.Context, taskID string) error {
	form := url.Values{}
	form.Set("taskId", taskID)
	return c.postForm(ctx, "/open/cancel-sql-task", form, nil)
}

// ---------------------------------------------------------------------------
// HTTP plumbing
// ---------------------------------------------------------------------------

func (c *Client) get(ctx context.Context, path string, query url.Values, dst any) error {
	u := c.baseURL + path + "?" + c.authQuery(query).Encode()
	return c.do(ctx, http.MethodGet, u, nil, "", dst)
}

func (c *Client) postForm(ctx context.Context, path string, form url.Values, dst any) error {
	u := c.baseURL + path + "?" + c.authQuery(nil).Encode()
	return c.do(ctx, http.MethodPost, u, strings.NewReader(form.Encode()),
		"application/x-www-form-urlencoded", dst)
}

func (c *Client) authQuery(extra url.Values) url.Values {
	q := url.Values{}
	q.Set("token", c.token)
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return q
}

// do issues an HTTP request with exponential-backoff retry on transient
// failures (5xx + 429 + network errors), parses the TA envelope, and writes
// the data body into dst if non-nil.
func (c *Client) do(ctx context.Context, method, url string, body io.Reader, contentType string, dst any) error {
	bodyBytes, err := drainBody(body)
	if err != nil {
		return err
	}

	op := func() error {
		req, err := http.NewRequestWithContext(ctx, method, url, bytesReader(bodyBytes))
		if err != nil {
			return backoff.Permanent(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return err // retryable
		}
		defer resp.Body.Close()

		buf, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}

		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("http %d: %s", resp.StatusCode, truncate(buf, 200))
		}
		if resp.StatusCode >= 400 {
			return backoff.Permanent(fmt.Errorf("http %d: %s", resp.StatusCode, truncate(buf, 200)))
		}

		var env envelope
		if err := json.Unmarshal(buf, &env); err != nil {
			return backoff.Permanent(fmt.Errorf("decode envelope: %w; body=%s", err, truncate(buf, 200)))
		}
		if env.ReturnCode != 0 {
			return backoff.Permanent(&APIError{Code: env.ReturnCode, Message: env.ReturnMessage})
		}
		if dst != nil && len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, dst); err != nil {
				return backoff.Permanent(fmt.Errorf("decode data: %w", err))
			}
		}
		return nil
	}

	policy := backoff.WithContext(backoff.NewExponentialBackOff(), ctx)
	return backoff.Retry(op, policy)
}

func isExpired(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	// The TA documentation does not enumerate an "expired" code, so we match
	// on common message fragments. False positives are not catastrophic — the
	// runner will re-submit, and #uuid dedup keeps data correct.
	msg := strings.ToLower(apiErr.Message)
	if strings.Contains(msg, "task") &&
		(strings.Contains(msg, "not found") ||
			strings.Contains(msg, "expired") ||
			strings.Contains(msg, "不存在") ||
			strings.Contains(msg, "已过期")) {
		return true
	}
	return false
}

func drainBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	return io.ReadAll(body)
}

func bytesReader(b []byte) io.Reader {
	if len(b) == 0 {
		return nil
	}
	return strings.NewReader(string(b))
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "...(truncated)"
}
