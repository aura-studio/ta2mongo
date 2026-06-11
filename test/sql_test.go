package test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/role/cli"
	"github.com/aura-studio/tango/internal/role/gateway"
)

// TestSQL_GatewayAndCLI exercises the SQL Data API through the gateway HTTP /sql
// endpoint and the cli sql sub-mode, sharing the same dao/sql core. Skipped when
// no MongoDB is reachable (see freshDB).
func TestSQL_GatewayAndCLI(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()
	ctx := context.Background()

	// --- gateway end: real HTTP handler over httptest ---
	srv, err := gateway.New(ctx, daoCfg, nil, nil, nil, gateway.Config{})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post := func(t *testing.T, sql string) *dao.SQLResult {
		t.Helper()
		body := `{"sql":` + jsonQuote(sql) + `}`
		resp, err := http.Post(ts.URL+"/sql", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /sql: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /sql status %d: %s", resp.StatusCode, b)
		}
		var out dao.SQLResult
		if err := bson.UnmarshalExtJSON(b, false, &out); err != nil {
			t.Fatalf("decode response: %v (%s)", err, b)
		}
		return &out
	}

	if r := post(t, `INSERT INTO widgets (name, n) VALUES ('x', 1)`); r.Kind != "insert" {
		t.Fatalf("gateway insert: %+v", r)
	}
	post(t, `INSERT INTO widgets (name, n) VALUES ('y', 2)`)

	if r := post(t, `SELECT * FROM widgets`); r.Kind != "select" || r.Rows == nil || len(*r.Rows) != 2 {
		t.Fatalf("gateway select: want 2 rows, got %+v", r)
	}

	// A 0-row SELECT still carries its rows key over the wire ("rows": []) —
	// the kind-owned pointer fields are always set (see dao/sql result.go).
	if r := post(t, `SELECT * FROM widgets WHERE name = 'nope'`); r.Rows == nil || len(*r.Rows) != 0 {
		t.Fatalf("gateway empty select: want non-nil empty rows, got %+v", r)
	}

	// Confirm via the independent verify handle.
	if n := count(t, db, "widgets"); n != 2 {
		t.Fatalf("verify count: want 2, got %d", n)
	}

	// --- cli end: same SQL path over stdin/stdout ---
	var buf bytes.Buffer
	in := strings.NewReader(`SELECT name, n FROM widgets WHERE name = 'x'`)
	if err := cli.RunSQL(ctx, daoCfg, in, &buf); err != nil {
		t.Fatalf("cli.RunSQL: %v", err)
	}
	var cliRes dao.SQLResult
	if err := bson.UnmarshalExtJSON(buf.Bytes(), false, &cliRes); err != nil {
		t.Fatalf("decode cli response: %v (%s)", err, buf.Bytes())
	}
	if cliRes.Rows == nil || len(*cliRes.Rows) != 1 || (*cliRes.Rows)[0]["name"] != "x" {
		t.Fatalf("cli select: %+v", cliRes)
	}
}

// jsonQuote returns a minimal JSON string literal for s (the test SQL has no
// characters needing escaping beyond quotes, but be safe with the encoder path).
func jsonQuote(s string) string {
	// SQL strings here use single quotes, so double-quote escaping is enough.
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
