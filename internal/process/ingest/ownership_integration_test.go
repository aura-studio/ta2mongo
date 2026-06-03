package ingest

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
)

// TestNewFromClient_DoesNotOwnClient is the ownership regression for the
// runtime borrow model: an Ingester built with NewFromClient borrows the
// caller's client, so Close must NOT disconnect it.
func TestNewFromClient_DoesNotOwnClient(t *testing.T) {
	pingMongo(t)

	ctx := context.Background()
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = client.Disconnect(ctx) }()

	dbName := fmt.Sprintf("tango_ingest_own_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	cfg := config.Config{Mongo: daomongo.Config{URI: testMongoURI + "/" + dbName}}

	ig, err := NewFromClient(client, cfg)
	if err != nil {
		t.Fatalf("NewFromClient: %v", err)
	}
	if err := ig.Close(); err != nil {
		t.Fatalf("Ingester.Close: %v", err)
	}

	// The borrowed client must still be alive after the ingester's Close.
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatalf("borrowed client was disconnected by Ingester.Close: %v", err)
	}
}
