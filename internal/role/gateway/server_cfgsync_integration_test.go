package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/process"
)

// cfgHarness drives a Server (with cfgsync enabled) and its HTTP face over a
// throwaway database, for the POST /config publish-face and end-to-end
// hot-swap tests.
type cfgHarness struct {
	t    *testing.T
	srv  *Server
	http *httptest.Server
	db   *mongo.Database
}

func cfgSetup(t *testing.T) (*cfgHarness, func()) {
	t.Helper()
	pingMongo(t)

	ctx := context.Background()
	dbName := fmt.Sprintf("tango_gw_cfg_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := spliceDB(testMongoURI, dbName)

	cfgsyncCfg := &cfgsync.Config{Enabled: true, PollInterval: 200 * time.Millisecond}
	srv, err := New(ctx, &dao.Config{Mongo: &daomongo.Config{URI: uri}},
		&process.Config{Mode: string(process.ModeSingle)}, nil, cfgsyncCfg, Config{})
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	if err := srv.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	verifyClient, _ := mongo.Connect(options.Client().ApplyURI(uri))
	db := verifyClient.Database(dbName)

	cleanup := func() {
		ts.Close()
		dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(dropCtx)
		_ = verifyClient.Disconnect(dropCtx)
		_ = srv.Close()
	}
	return &cfgHarness{t: t, srv: srv, http: ts, db: db}, cleanup
}

// postConfig POSTs a config document to /config and returns the HTTP status and
// decoded {version} (version is 0 when the body is not a version envelope).
func (h *cfgHarness) postConfig(doc bson.M) (int, int64) {
	h.t.Helper()
	body, _ := json.Marshal(doc)
	resp, err := http.Post(h.http.URL+"/config", "application/json", bytes.NewReader(body))
	if err != nil {
		h.t.Fatalf("POST /config: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var env struct {
		Version int64 `json:"version"`
	}
	_ = json.Unmarshal(raw, &env)
	return resp.StatusCode, env.Version
}

func TestServer_PostConfig_PublishesAndIncrements(t *testing.T) {
	h, cleanup := cfgSetup(t)
	defer cleanup()

	code, v1 := h.postConfig(bson.M{"filter": bson.M{"include": []string{`#type == "track"`}}})
	if code != http.StatusOK {
		t.Fatalf("first publish: status = %d, want 200", code)
	}
	code, v2 := h.postConfig(bson.M{"filter": bson.M{"include": []string{`#type == "user_set"`}}})
	if code != http.StatusOK {
		t.Fatalf("second publish: status = %d, want 200", code)
	}
	if v2 <= v1 {
		t.Fatalf("version not monotonic across publishes: v1=%d v2=%d", v1, v2)
	}
}

func TestServer_PostConfig_RejectsOffAllowlist(t *testing.T) {
	h, cleanup := cfgSetup(t)
	defer cleanup()

	code, _ := h.postConfig(bson.M{"dao": bson.M{"mongo": bson.M{"uri": "x"}}})
	if code != http.StatusBadRequest {
		t.Fatalf("off-allowlist publish: status = %d, want 400", code)
	}
	code, _ = h.postConfig(bson.M{"filter": bson.M{"include": []string{`#type ==== "x"`}}})
	if code != http.StatusBadRequest {
		t.Fatalf("uncompilable filter: status = %d, want 400", code)
	}
}

// TestServer_PostConfig_EndToEnd_HotSwap publishes a filter through the HTTP face
// and asserts the gateway's own cfgsync watcher hot-swaps the live reporting
// filter — observed purely through /upload behaviour (a filtered line is not
// written; a kept line is). It starts the watcher via the engine (white-box) so
// no socket needs binding for Run.
func TestServer_PostConfig_EndToEnd_HotSwap(t *testing.T) {
	h, cleanup := cfgSetup(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.srv.engine.StartCfgsync(ctx) // white-box: same call Run makes

	// Publish include=track via the HTTP face.
	if code, _ := h.postConfig(bson.M{"filter": bson.M{"include": []string{`#type == "track"`}}}); code != http.StatusOK {
		t.Fatalf("publish status = %d", code)
	}

	// Wait for the watcher to apply it, observed via upload behaviour: a user_set
	// line is now filtered out (no user write), a track line is kept.
	deadline := time.Now().Add(5 * time.Second)
	applied := false
	for time.Now().Before(deadline) {
		res, err := h.srv.Upload(ctx, []string{
			`{"#type":"user_set","#time":"2024-01-01","#uuid":"cfg-u","#account_id":"a","properties":{"name":"X"}}`,
		})
		if err != nil {
			t.Fatalf("upload: %v", err)
		}
		if res.Filtered == 1 && res.UserWrites == 0 {
			applied = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !applied {
		t.Fatal("published include=track filter never hot-swapped (user_set was not filtered)")
	}

	// A track line passes the same live filter and is written.
	res, err := h.srv.Upload(ctx, []string{
		`{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"cfg-e","#account_id":"a"}`,
	})
	if err != nil {
		t.Fatalf("upload track: %v", err)
	}
	if res.EventWrites != 1 || res.Filtered != 0 {
		t.Fatalf("track line should pass include=track filter: %+v", res)
	}
}
