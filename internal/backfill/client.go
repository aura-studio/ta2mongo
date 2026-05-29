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
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"golang.org/x/net/proxy"
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

// NewHTTPClient builds an HTTP client honouring an optional proxy URL. Empty
// proxyURL means direct connection. http://, https://, and socks5:// schemes
// are accepted; embedded user:pass is parsed for both HTTP CONNECT and
// SOCKS5 auth.
func NewHTTPClient(proxyURL string, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	if proxyURL == "" {
		return &http.Client{Timeout: timeout}, nil
	}

	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy url: %w", err)
	}

	transport := &http.Transport{
		MaxIdleConns:        16,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}

	switch u.Scheme {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
	case "socks5":
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		dialer, err := proxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 15 * time.Second})
		if err != nil {
			return nil, fmt.Errorf("socks5 dialer: %w", err)
		}
		cd, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, fmt.Errorf("socks5 dialer does not implement ContextDialer")
		}
		transport.DialContext = cd.DialContext
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}

	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// SubmitResult is the unmarshalled "data" body of /open/submit-sql.
type SubmitResult struct {
	TaskID string `json:"taskId"`
}

// SubmitSQL submits an async SQL query and returns the assigned task ID.
func (c *Client) SubmitSQL(ctx context.Context, sql string) (string, error) {
	form := url.Values{}
	form.Set("sql", sql)

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

// ResultPage fetches a single page of results for an async SQL task.
//
// The TA result-page endpoint returns a streaming NDJSON body where each line
// is a single result row encoded as a JSON array of column values. There is
// no envelope and no embedded metadata: headers + page count are obtained
// once from TaskInfo and reused on every page fetch.
//
// On HTTP-level errors (4xx/5xx) the response body may instead be a single
// envelope JSON with a non-zero return_code; we detect and surface that as
// an APIError, mapping the "task not found / expired" variants to
// ErrTaskExpired.
func (c *Client) ResultPage(ctx context.Context, taskID string, pageID, pageSize int) (*ResultPageResult, error) {
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
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, body)
	}

	return parseResultPageStream(resp.Body)
}

// parseResultPageStream consumes an NDJSON result-page body, returning the
// accumulated rows. An envelope-shaped JSON object on the first token is
// treated as an API error (typical for "task expired" responses returned
// with HTTP 200).
func parseResultPageStream(body io.Reader) (*ResultPageResult, error) {
	dec := json.NewDecoder(body)
	dec.UseNumber()

	var rows [][]interface{}
	for dec.More() {
		// Peek at the next token; if it is an opening brace we have an
		// envelope-style error rather than a row.
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("decode result-page: %w", err)
		}
		if d, ok := tok.(json.Delim); ok && d == '{' {
			// Re-read the rest of the object as an envelope.
			var env envelope
			env.Data = json.RawMessage("null")
			// We've consumed the '{' already; gather key/value pairs
			// manually via the decoder until we hit the matching '}'.
			obj, err := readObjectRemainder(dec)
			if err != nil {
				return nil, fmt.Errorf("decode envelope error: %w", err)
			}
			if rc, ok := obj["return_code"]; ok {
				switch v := rc.(type) {
				case json.Number:
					n, _ := v.Int64()
					env.ReturnCode = int(n)
				case float64:
					env.ReturnCode = int(v)
				}
			}
			if rm, ok := obj["return_message"].(string); ok {
				env.ReturnMessage = rm
			}
			if env.ReturnCode != 0 {
				apiErr := &APIError{Code: env.ReturnCode, Message: env.ReturnMessage}
				if isExpired(apiErr) {
					return nil, ErrTaskExpired
				}
				return nil, apiErr
			}
			// Envelope with return_code=0 and no rows yet — continue, the
			// real row arrays should follow.
			continue
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return nil, fmt.Errorf("decode result-page: unexpected token %v", tok)
		}
		row, err := readArrayRemainder(dec)
		if err != nil {
			return nil, fmt.Errorf("decode row %d: %w", len(rows), err)
		}
		rows = append(rows, row)
	}
	return &ResultPageResult{Rows: rows}, nil
}

// readObjectRemainder reads JSON object members until the closing '}',
// returning a generic map. The decoder must have already consumed the
// opening '{'.
func readObjectRemainder(dec *json.Decoder) (map[string]any, error) {
	out := make(map[string]any)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key, got %T", keyTok)
		}
		var val any
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out[key] = val
	}
	// Consume the closing '}'.
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

// readArrayRemainder reads JSON array elements until the closing ']'. The
// decoder must have already consumed the opening '['.
func readArrayRemainder(dec *json.Decoder) ([]interface{}, error) {
	var out []interface{}
	for dec.More() {
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
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
