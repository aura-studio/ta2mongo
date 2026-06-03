package store

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const testMongoURI = "mongodb://localhost:27017"

// testSetup connects to local MongoDB, creates a random test database,
// and returns a Store and cleanup function. The cleanup function drops the
// database and disconnects.
func testSetup(t *testing.T) (*Store, *mongo.Database, func()) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientOpts := options.Client().
		ApplyURI(testMongoURI).
		SetServerSelectionTimeout(3 * time.Second).
		SetConnectTimeout(3 * time.Second)

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		t.Skipf("MongoDB not available at %s: %v", testMongoURI, err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("MongoDB not available at %s: %v", testMongoURI, err)
	}

	dbName := fmt.Sprintf("tango_test_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	db := client.Database(dbName)

	st := New(db, Config{MaxElapsedTime: 5 * time.Second})

	cleanup := func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dropCancel()
		_ = db.Drop(dropCtx)
		_ = client.Disconnect(dropCtx)
	}

	return st, db, cleanup
}
