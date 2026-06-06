package tests

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

// TestDataAPI_GatewayAndCLI exercises the Mongo Data API through two of its three
// ends — the gateway HTTP /data endpoint and the cli data sub-mode — sharing the
// same functional core. Skipped when no MongoDB is reachable (see freshDB).
func TestDataAPI_GatewayAndCLI(t *testing.T) {
	daoCfg, db, cleanup := freshDB(t)
	defer cleanup()
	ctx := context.Background()

	// --- gateway end: real HTTP handler over httptest ---
	srv, err := gateway.New(ctx, daoCfg, nil, nil, gateway.Config{})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	defer srv.Close()
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post := func(t *testing.T, body string) *dao.DataResponse {
		t.Helper()
		resp, err := http.Post(ts.URL+"/data", "application/ejson", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST /data: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /data status %d: %s", resp.StatusCode, b)
		}
		var out dao.DataResponse
		if err := bson.UnmarshalExtJSON(b, false, &out); err != nil {
			t.Fatalf("decode response: %v (%s)", err, b)
		}
		return &out
	}

	if r := post(t, `{"action":"insertOne","collection":"widgets","document":{"name":"x","n":1}}`); r.InsertedID == nil {
		t.Fatal("gateway insertOne: nil insertedId")
	}
	post(t, `{"action":"insertOne","collection":"widgets","document":{"name":"y","n":2}}`)

	if r := post(t, `{"action":"find","collection":"widgets","filter":{},"sort":{"n":1}}`); len(r.Documents) != 2 {
		t.Fatalf("gateway find: want 2 docs, got %d", len(r.Documents))
	}

	// Confirm the writes landed, via the independent verify handle.
	if n := count(t, db, "widgets"); n != 2 {
		t.Fatalf("verify count: want 2, got %d", n)
	}

	// --- cli end: same data path over stdin/stdout ---
	var buf bytes.Buffer
	in := strings.NewReader(`{"action":"findOne","collection":"widgets","filter":{"name":"x"}}`)
	if err := cli.RunData(ctx, daoCfg, in, &buf); err != nil {
		t.Fatalf("cli.RunData: %v", err)
	}
	var cliResp dao.DataResponse
	if err := bson.UnmarshalExtJSON(buf.Bytes(), false, &cliResp); err != nil {
		t.Fatalf("decode cli response: %v (%s)", err, buf.Bytes())
	}
	if cliResp.Document["name"] != "x" {
		t.Fatalf("cli findOne: want name=x, got %v", cliResp.Document)
	}
}
