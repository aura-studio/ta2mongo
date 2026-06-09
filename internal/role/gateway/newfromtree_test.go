package gateway

// v1.5.1 increment test (doc/test2.md G3): gateway.NewFromTree slices role.gateway
// (Addr) plus the engine branches and delegates to New. We assert the sliced
// gwCfg.Addr is correct and that the resulting Server's HTTP routes (/healthz,
// /upload) work — the same handler Role.Run mounts. The tree is built with
// cfgtree.New to avoid the config -> role -> gateway import cycle. Reuses
// testMongoURI / spliceDB / pingMongo from server_integration_test.go.

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/cfgtree"
)

func TestNewFromTree_G3_GatewayAddrAndHTTP(t *testing.T) {
	pingMongo(t)

	dbName := fmt.Sprintf("tango_gw_nft_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := spliceDB(testMongoURI, dbName)

	verify, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("verify connect: %v", err)
	}
	db := verify.Database(dbName)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(ctx)
		_ = verify.Disconnect(ctx)
	}()

	const addr = "127.0.0.1:18091"
	tree := cfgtree.New(map[string]any{
		"role":    map[string]any{"gateway": map[string]any{"addr": addr}},
		"dao":     map[string]any{"mongo": map[string]any{"uri": uri}},
		"process": map[string]any{"mode": "single"},
	})

	srv, gwCfg, err := NewFromTree(context.Background(), tree)
	if err != nil {
		t.Fatalf("NewFromTree: %v", err)
	}
	defer srv.Close()

	// G3a: the Addr Role.Run would listen on is sliced correctly from config.
	if gwCfg.Addr != addr {
		t.Errorf("gwCfg.Addr = %q, want %q", gwCfg.Addr, addr)
	}

	if err := srv.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// G3b: the HTTP routes Role.Run mounts work.
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// /healthz -> 200
	hresp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = hresp.Body.Close()
	if hresp.StatusCode != http.StatusOK {
		t.Errorf("/healthz status = %d, want 200", hresp.StatusCode)
	}

	// /upload {"lines":[...]} -> 200 and the event lands in Mongo.
	body := `{"lines":["{\"#type\":\"track\",\"#event_name\":\"g3\",\"#time\":\"2026-06-09\",\"#uuid\":\"g3-e1\",\"#account_id\":\"a\"}"]}`
	uresp, err := http.Post(ts.URL+"/upload", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /upload: %v", err)
	}
	rb, _ := io.ReadAll(uresp.Body)
	_ = uresp.Body.Close()
	if uresp.StatusCode != http.StatusOK {
		t.Fatalf("/upload status = %d, want 200 (body %s)", uresp.StatusCode, rb)
	}

	n, err := db.Collection("event").CountDocuments(context.Background(), bson.M{"#uuid": "g3-e1"})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("event g3-e1 count = %d, want 1 (upload did not ingest)", n)
	}
}
