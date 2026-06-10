package gateway

// GET /config (query face) + POST /config?mode=set|append (publish modes).
// Integration — needs TANGO_TEST_MONGO_URI (pingMongo skips otherwise); each
// test uses a throwaway db via the shared spliceDB helper.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/process"
)

func TestConfigEndpoint_GetAndPublishModes(t *testing.T) {
	pingMongo(t)
	ctx := context.Background()
	dbName := fmt.Sprintf("tango_gw_cfg_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := spliceDB(testMongoURI, dbName)

	srv, err := New(ctx, &dao.Config{Mongo: &daomongo.Config{URI: uri}},
		&process.Config{Mode: string(process.ModeSingle)}, nil, nil, Config{})
	if err != nil {
		t.Fatalf("gateway.New: %v", err)
	}
	defer srv.Close()
	vc, _ := mongo.Connect(options.Client().ApplyURI(uri))
	defer func() {
		dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = vc.Database(dbName).Drop(dctx)
		_ = vc.Disconnect(dctx)
	}()

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body := func(resp *http.Response) string {
		t.Helper()
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return string(b)
	}

	// 1) GET before anything published → 404.
	resp, err := http.Get(ts.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	if b := body(resp); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET empty = %d %q, want 404", resp.StatusCode, b)
	}

	// 2) POST (default = set) seeds include[a] + exclude[x] → version 1.
	resp, err = http.Post(ts.URL+"/config", "application/json",
		strings.NewReader(`{"filter":{"include":["#type == \"a\""],"exclude":["#type == \"x\""]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if b := body(resp); resp.StatusCode != http.StatusOK || !strings.Contains(b, `"version":1`) {
		t.Fatalf("POST set = %d %q, want 200 version 1", resp.StatusCode, b)
	}

	// 3) POST ?mode=append with only include[b]: include unioned, exclude kept.
	resp, err = http.Post(ts.URL+"/config?mode=append", "application/json",
		strings.NewReader(`{"filter":{"include":["#type == \"b\""]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if b := body(resp); resp.StatusCode != http.StatusOK || !strings.Contains(b, `"version":2`) {
		t.Fatalf("POST append = %d %q, want 200 version 2", resp.StatusCode, b)
	}

	// 4) GET returns the merged doc with its version.
	resp, err = http.Get(ts.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	raw := body(resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after publish = %d %q, want 200", resp.StatusCode, raw)
	}
	var doc struct {
		Version int64 `json:"version"`
		Filter  struct {
			Include []string `json:"include"`
			Exclude []string `json:"exclude"`
		} `json:"filter"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("GET body %q not parseable: %v", raw, err)
	}
	if doc.Version != 2 {
		t.Errorf("GET version = %d, want 2", doc.Version)
	}
	if len(doc.Filter.Include) != 2 || doc.Filter.Include[0] != `#type == "a"` || doc.Filter.Include[1] != `#type == "b"` {
		t.Errorf("GET include = %v, want [a b] (append union)", doc.Filter.Include)
	}
	if len(doc.Filter.Exclude) != 1 || doc.Filter.Exclude[0] != `#type == "x"` {
		t.Errorf("GET exclude = %v, want [x] preserved by append", doc.Filter.Exclude)
	}

	// 5) Unknown mode → 400; non-GET/POST method → 405.
	resp, err = http.Post(ts.URL+"/config?mode=merge", "application/json", strings.NewReader(`{"filter":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	if b := body(resp); resp.StatusCode != http.StatusBadRequest || !strings.Contains(b, "unknown mode") {
		t.Fatalf("POST mode=merge = %d %q, want 400 unknown mode", resp.StatusCode, b)
	}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/config", strings.NewReader(`{}`))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if b := body(resp); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT = %d %q, want 405", resp.StatusCode, b)
	}
}
