// Tests for the cfgsync central-config faces on the public client —
// PublishConfig / AppendConfig / FetchConfig — plus the cfgsync.* options
// wiring. The faces relay to the same cores as gateway POST/GET /config
// (cfgsync.Publish / PublishAppend / Fetch), so these tests pin the facade
// contract: validation gates run before any write, versions are monotonic,
// fetch round-trips the published document, and "never published" is the
// (nil, nil) sentinel rather than an error.
//
// The integration test runs against TANGO_TEST_MONGO_URI (falling back to a
// local mongod, skipping when unreachable) on a throwaway database injected
// into the URI path and dropped on cleanup, mirroring ultra_concurrent_test.go.
package client

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	mopt "go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/cfgsync"
)

// The client must address the same central collection/document the daemon and
// gateway watchers track by default, or a publish through the client would
// land where no watcher looks.
func TestDefaultOptions_Cfgsync(t *testing.T) {
	o := buildOptions()
	if o.cfgsync.Collection != cfgsync.DefaultCollection {
		t.Errorf("cfgsync.collection default = %q, want %q", o.cfgsync.Collection, cfgsync.DefaultCollection)
	}
	if o.cfgsync.DocumentID != cfgsync.DefaultDocumentID {
		t.Errorf("cfgsync.documentID default = %q, want %q", o.cfgsync.DocumentID, cfgsync.DefaultDocumentID)
	}
	if o.cfgsync.Enabled {
		t.Error("cfgsync.enabled default = true, want false (the client never starts the watcher)")
	}
}

// A gateway-compatible config's cfgsync.* section steers the publish/fetch
// faces (documentID, intervals), same as it steers the binary roles — EXCEPT
// the collection, which is a fixed convention forced to DefaultCollection.
func TestWithConfigBytes_Cfgsync(t *testing.T) {
	o := buildOptions(WithConfigBytes([]byte(`
dao:
  mongo:
    uri: mongodb://localhost:27017/tango
cfgsync:
  collection: my_config
  documentID: mydoc
`)))
	if o.err != nil {
		t.Fatalf("unexpected err: %v", o.err)
	}
	// collection is NOT steerable: cfgsync.collection is ignored and forced.
	if o.cfgsync.Collection != cfgsync.DefaultCollection {
		t.Errorf("cfgsync.collection = %q, want forced %q", o.cfgsync.Collection, cfgsync.DefaultCollection)
	}
	if o.cfgsync.DocumentID != "mydoc" {
		t.Errorf("cfgsync.documentID = %q, want mydoc", o.cfgsync.DocumentID)
	}
	// A leaf the config omitted keeps the engine default (re-ApplyDefaults).
	if o.cfgsync.PollInterval != cfgsync.DefaultPollInterval {
		t.Errorf("cfgsync.pollInterval = %v, want default %v", o.cfgsync.PollInterval, cfgsync.DefaultPollInterval)
	}
}

// fetchedVersion extracts the monotonic version from a fetched config document
// regardless of the numeric type the driver decoded (int32/int64/float64),
// mirroring cfgsync's own tolerant read.
func fetchedVersion(t *testing.T, doc map[string]any) int64 {
	t.Helper()
	switch v := doc["version"].(type) {
	case int32:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		t.Fatalf("version has unexpected type %T (%v)", doc["version"], doc["version"])
		return 0
	}
}

// TestConfigFacade_PublishFetchAppend drives the full publish → fetch → append
// → fetch cycle through the public client against a real MongoDB.
func TestConfigFacade_PublishFetchAppend(t *testing.T) {
	ultraPingMongo(t)

	dbName := fmt.Sprintf("tango_client_cfg_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
	uri := ultraWithDBName(ultraMongoURI(), dbName)

	// Independent verify connection; drops the throwaway db on cleanup.
	vc, err := mongo.Connect(mopt.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("verify connect: %v", err)
	}
	db := vc.Database(dbName)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(ctx)
		_ = vc.Disconnect(ctx)
	})

	c, err := New(WithDaoMongoURI(uri))
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx := context.Background()

	// Never published → the (nil, nil) sentinel, not an error.
	if doc, err := c.FetchConfig(ctx); err != nil || doc != nil {
		t.Fatalf("FetchConfig before publish = (%v, %v), want (nil, nil)", doc, err)
	}

	// set: whole-tree replace, first version.
	v1, err := c.PublishConfig(ctx, map[string]any{
		"filter": map[string]any{"include": []string{`#type == "track"`}},
	})
	if err != nil {
		t.Fatalf("PublishConfig: %v", err)
	}
	if v1 < 1 {
		t.Fatalf("publish version = %d, want >= 1", v1)
	}

	doc, err := c.FetchConfig(ctx)
	if err != nil {
		t.Fatalf("FetchConfig: %v", err)
	}
	if doc == nil {
		t.Fatal("FetchConfig = nil after publish")
	}
	if got := fetchedVersion(t, doc); got != v1 {
		t.Errorf("fetched version = %d, want %d", got, v1)
	}

	// append: union merge under the version guard, version bumps by one.
	v2, err := c.AppendConfig(ctx, map[string]any{
		"filter": map[string]any{"include": []string{`#type == "user_set"`}},
	})
	if err != nil {
		t.Fatalf("AppendConfig: %v", err)
	}
	if v2 != v1+1 {
		t.Errorf("append version = %d, want %d", v2, v1+1)
	}

	doc, err = c.FetchConfig(ctx)
	if err != nil || doc == nil {
		t.Fatalf("FetchConfig after append = (%v, %v)", doc, err)
	}
	// The facade contract: only plain Go containers, never driver types.
	f, ok := doc["filter"].(map[string]any)
	if !ok {
		t.Fatalf("fetched filter has type %T, want map[string]any", doc["filter"])
	}
	inc, ok := f["include"].([]any)
	if !ok || len(inc) != 2 {
		t.Fatalf("include after append = %v, want the stored rule plus the appended one", f["include"])
	}
	if inc[0] != `#type == "track"` || inc[1] != `#type == "user_set"` {
		t.Errorf("include after append = %v, want stored-first union", inc)
	}

	// The validation gate runs before any write: an off-allowlist subtree is
	// rejected and the stored document keeps its version.
	if _, err := c.PublishConfig(ctx, map[string]any{"nope": 1}); err == nil {
		t.Error("PublishConfig accepted an off-allowlist document")
	}
	if doc, err := c.FetchConfig(ctx); err != nil || fetchedVersion(t, doc) != v2 {
		t.Errorf("rejected publish must not move the version: (%v, %v), want version %d", doc, err, v2)
	}
}
