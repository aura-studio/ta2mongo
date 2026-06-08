package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
)

// testMongoURI honors TANGO_TEST_MONGO_URI (e.g. Amazon DocumentDB) and falls
// back to a local mongod, so the cfgsync cli-face suite runs against the real
// cluster on EC2 as well as locally.
var testMongoURI = mongoBaseURI()

func mongoBaseURI() string {
	if u := os.Getenv("TANGO_TEST_MONGO_URI"); u != "" {
		return u
	}
	return "mongodb://localhost:27017"
}

// spliceDB inserts /dbName before the query string of a mongo URI, preserving
// the tls/retryWrites params a DocumentDB URI carries (plain concatenation would
// corrupt them).
func spliceDB(uri, dbName string) string {
	scheme := "mongodb://"
	if strings.HasPrefix(uri, "mongodb+srv://") {
		scheme = "mongodb+srv://"
	}
	rest := strings.TrimPrefix(uri, scheme)
	query := ""
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		query = rest[i:]
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return scheme + rest + "/" + dbName + query
}

// pingMongo skips the test when no MongoDB is reachable.
func pingMongo(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(testMongoURI).
		SetServerSelectionTimeout(2 * time.Second).SetConnectTimeout(2 * time.Second))
	if err != nil {
		t.Skipf("MongoDB not available: %v", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("MongoDB not available: %v", err)
	}
	_ = client.Disconnect(ctx)
}

func cliDaoCfg(t *testing.T) (*dao.Config, func()) {
	t.Helper()
	pingMongo(t)
	dbName := fmt.Sprintf("tango_cli_cfg_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := spliceDB(testMongoURI, dbName)
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client, _ := mongo.Connect(options.Client().ApplyURI(uri))
		_ = client.Database(dbName).Drop(ctx)
		_ = client.Disconnect(ctx)
	}
	return &dao.Config{Mongo: &daomongo.Config{URI: uri}}, cleanup
}

// TestRunConfig_PublishesFromStdin exercises the cli function=config face: a
// config document on stdin is published and {"version":N} written to stdout,
// with the version incrementing across publishes.
func TestRunConfig_PublishesFromStdin(t *testing.T) {
	daoCfg, cleanup := cliDaoCfg(t)
	defer cleanup()
	ctx := context.Background()
	cfgsyncCfg := &cfgsync.Config{}

	readVersion := func(out string) int64 {
		var env struct {
			Version int64 `json:"version"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("decode stdout %q: %v", out, err)
		}
		return env.Version
	}

	var out1 bytes.Buffer
	in1 := strings.NewReader(`{"filter":{"include":["#type == \"track\""]}}`)
	if err := RunConfig(ctx, daoCfg, cfgsyncCfg, in1, &out1); err != nil {
		t.Fatalf("RunConfig 1: %v", err)
	}
	v1 := readVersion(out1.String())

	var out2 bytes.Buffer
	in2 := strings.NewReader(`{"filter":{"include":["#type == \"user_set\""]}}`)
	if err := RunConfig(ctx, daoCfg, cfgsyncCfg, in2, &out2); err != nil {
		t.Fatalf("RunConfig 2: %v", err)
	}
	v2 := readVersion(out2.String())

	if v2 <= v1 {
		t.Fatalf("version not monotonic: v1=%d v2=%d", v1, v2)
	}
}

// TestRunConfig_RejectsOffAllowlist asserts the cli face rejects a bad config
// (off-allowlist subtree) before writing — same gate as the other faces.
func TestRunConfig_RejectsOffAllowlist(t *testing.T) {
	daoCfg, cleanup := cliDaoCfg(t)
	defer cleanup()

	var out bytes.Buffer
	in := strings.NewReader(`{"dao":{"mongo":{"uri":"x"}}}`)
	if err := RunConfig(context.Background(), daoCfg, &cfgsync.Config{}, in, &out); err == nil {
		t.Fatal("expected RunConfig to reject an off-allowlist document")
	}
}
