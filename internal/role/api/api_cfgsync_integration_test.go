package api

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
)

const testMongoURI = "mongodb://localhost:27017"

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

func newEngine(t *testing.T) (*Engine, func()) {
	t.Helper()
	pingMongo(t)
	dbName := fmt.Sprintf("tango_api_cfg_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := testMongoURI + "/" + dbName
	eng, err := New(context.Background(),
		&dao.Config{Mongo: &daomongo.Config{URI: uri}}, nil, nil, &cfgsync.Config{})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		client, _ := mongo.Connect(options.Client().ApplyURI(uri))
		_ = client.Database(dbName).Drop(ctx)
		_ = client.Disconnect(ctx)
		_ = eng.Close()
	}
	return eng, cleanup
}

// TestEngine_PublishConfig exercises the api face of cfgsync.Publish: a valid
// document publishes with a monotonically increasing version; an off-allowlist
// document is rejected.
func TestEngine_PublishConfig(t *testing.T) {
	eng, cleanup := newEngine(t)
	defer cleanup()
	ctx := context.Background()

	v1, err := eng.PublishConfig(ctx, bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}})
	if err != nil {
		t.Fatalf("PublishConfig 1: %v", err)
	}
	v2, err := eng.PublishConfig(ctx, bson.M{"filter": bson.M{"include": bson.A{`#type == "user_set"`}}})
	if err != nil {
		t.Fatalf("PublishConfig 2: %v", err)
	}
	if v2 <= v1 {
		t.Fatalf("version not monotonic: v1=%d v2=%d", v1, v2)
	}

	if _, err := eng.PublishConfig(ctx, bson.M{"dao": bson.M{"mongo": bson.M{"uri": "x"}}}); err == nil {
		t.Fatal("expected off-allowlist publish to be rejected")
	}
}
