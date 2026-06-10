// SDK-8 (doc/ultra_test.md §9.1): concurrent Upload safety on a single shared
// Client under -race, plus the EnsureIndexes/Close forwarding the same row
// covers.
//
// Client documents itself as "safe for concurrent use; the underlying MongoDB
// driver manages the connection pool", and api.Engine documents "each upload
// run uses its own stats collector". This test hammers exactly that contract:
// one Client, 16 goroutines, each Upload()ing 50 distinct track events through
// the shared engine/dao/connection pool. Every #uuid is unique
// (ultra-conc-g<g>-i<i>), so the event-collection count is exact — no upsert
// dedup ambiguity — and each per-run Result must independently report its own
// 50 lines / 50 event writes (per-run stats isolation).
//
// Runs against TANGO_TEST_MONGO_URI (skips when unreachable) on a throwaway
// database injected into the URI path, dropped on cleanup. Covered for the
// default "batch" strategy and the worker-pool "pipeline" strategy — the most
// concurrency-prone path.
package client

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	mopt "go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	ultraGoroutines = 16
	ultraPerG       = 50
)

// ultraMongoURI honors TANGO_TEST_MONGO_URI (the docker-compose stack sets
// mongodb://mongo:27017) and falls back to a local mongod.
func ultraMongoURI() string {
	if u := os.Getenv("TANGO_TEST_MONGO_URI"); u != "" {
		return u
	}
	return "mongodb://localhost:27017"
}

// ultraWithDBName injects the throwaway database into the URI path before any
// "?query" component, so a DocumentDB URI's tls/retryWrites params survive
// (plain uri+"/"+db concatenation would corrupt them). Same splice as
// test/integration_test.go's withDBName.
func ultraWithDBName(uri, dbName string) string {
	base, query := uri, ""
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		base, query = uri[:i], uri[i:]
	}
	return strings.TrimRight(base, "/") + "/" + dbName + query
}

// ultraPingMongo skips the test when no MongoDB is reachable, matching the
// pattern of the repo's other TANGO_TEST_MONGO_URI integration tests.
func ultraPingMongo(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	vc, err := mongo.Connect(mopt.Client().ApplyURI(ultraMongoURI()).
		SetServerSelectionTimeout(2 * time.Second).SetConnectTimeout(2 * time.Second))
	if err != nil {
		t.Skipf("MongoDB not available: %v", err)
	}
	if err := vc.Ping(ctx, nil); err != nil {
		_ = vc.Disconnect(ctx)
		t.Skipf("MongoDB not available: %v", err)
	}
	_ = vc.Disconnect(ctx)
}

func ultraCount(t *testing.T, db *mongo.Database, coll string) int64 {
	t.Helper()
	n, err := db.Collection(coll).CountDocuments(context.Background(), bson.M{})
	if err != nil {
		t.Fatalf("count %s: %v", coll, err)
	}
	return n
}

// TestUltraConcurrentUpload is SDK-8: 16 goroutines share one Client and each
// Upload()s 50 distinct track events; no goroutine may error, every Result must
// carry its own run's exact counts, and the event collection must hold exactly
// 16*50 = 800 documents. Run with -race: the point is the race detector over
// the shared engine and connection pool.
func TestUltraConcurrentUpload(t *testing.T) {
	t.Run("batch_default", func(t *testing.T) { ultraRunConcurrentUpload(t) })
	t.Run("pipeline", func(t *testing.T) {
		ultraRunConcurrentUpload(t, WithProcessMode("pipeline"))
	})
}

func ultraRunConcurrentUpload(t *testing.T, extra ...Option) {
	ultraPingMongo(t)

	dbName := fmt.Sprintf("tango_client_ultra_%d_%d", time.Now().UnixNano(), rand.Intn(100000))
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

	c, err := New(append([]Option{WithDaoMongoURI(uri)}, extra...)...)
	if err != nil {
		t.Fatalf("client.New: %v", err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = c.Close()
		}
	})

	// SDK-8 forwarding: EnsureIndexes relays to store.EnsureIndexes (idempotent).
	if err := c.EnsureIndexes(context.Background()); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	var (
		wg      sync.WaitGroup
		results [ultraGoroutines]Result
		errs    [ultraGoroutines]error
	)
	for g := 0; g < ultraGoroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			lines := make([]string, 0, ultraPerG)
			for i := 0; i < ultraPerG; i++ {
				lines = append(lines, fmt.Sprintf(
					`{"#type":"track","#event_name":"ultra_conc","#time":"2026-06-10","#uuid":"ultra-conc-g%02d-i%02d","#account_id":"acc-ultra-%d"}`,
					g, i, g%4)) // unique #uuid per goroutine+i; 4 distinct accounts
			}
			results[g], errs[g] = c.Upload(context.Background(), lines...)
		}(g)
	}
	wg.Wait()

	for g := 0; g < ultraGoroutines; g++ {
		if errs[g] != nil {
			t.Fatalf("goroutine %d: Upload error: %v", g, errs[g])
		}
		// Per-run stats isolation: each Result reports exactly its own run.
		r := results[g]
		if r.Lines != ultraPerG {
			t.Errorf("goroutine %d: Result.Lines = %d, want %d", g, r.Lines, ultraPerG)
		}
		if r.EventWrites != ultraPerG {
			t.Errorf("goroutine %d: Result.EventWrites = %d, want %d", g, r.EventWrites, ultraPerG)
		}
		if r.UserWrites != 0 || r.DeadLetters != 0 || r.Filtered != 0 {
			t.Errorf("goroutine %d: Result = %+v, want UserWrites/DeadLetters/Filtered all 0", g, r)
		}
	}

	want := int64(ultraGoroutines * ultraPerG)
	if got := ultraCount(t, db, "event"); got != want {
		t.Errorf("event count = %d, want exactly %d", got, want)
	}
	if got := ultraCount(t, db, "dead_letter"); got != 0 {
		t.Errorf("dead_letter count = %d, want 0", got)
	}
	if got := ultraCount(t, db, "user"); got != 0 {
		t.Errorf("user count = %d, want 0", got)
	}

	// SDK-8 forwarding: Close relays to engine.Close (mongo disconnect) cleanly.
	closed = true
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
