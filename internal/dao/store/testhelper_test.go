package store

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// testMongoURI returns the connection string for the integration tests: the
// TANGO_TEST_MONGO_URI env var if set, otherwise a local MongoDB. Point it at an
// Amazon DocumentDB endpoint (including its own tls=...&retryWrites=false query
// params) to run the suite against the real engine — the only way to catch
// engine incompatibilities such as the aggregation-pipeline-update rejection the
// _ts filter-guard write models fix. TLS is configured entirely from the URI.
func testMongoURI() string {
	if uri := os.Getenv("TANGO_TEST_MONGO_URI"); uri != "" {
		return uri
	}
	return "mongodb://localhost:27017"
}

// testSetup connects to MongoDB/DocumentDB, creates a random test database,
// and returns a Store and cleanup function. The cleanup function drops the
// database and disconnects.
func testSetup(t *testing.T) (*Store, *mongo.Database, func()) {
	t.Helper()

	uri := testMongoURI()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientOpts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(3 * time.Second).
		SetConnectTimeout(3 * time.Second)

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		t.Skipf("MongoDB not available at %s: %v", uri, err)
	}

	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("MongoDB not available at %s: %v", uri, err)
	}

	dbName := fmt.Sprintf("tango_test_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	db := client.Database(dbName)

	st := New(db, &Config{MaxElapsedTime: 5 * time.Second})

	cleanup := func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dropCancel()
		_ = db.Drop(dropCtx)
		_ = client.Disconnect(dropCtx)
	}

	return st, db, cleanup
}
