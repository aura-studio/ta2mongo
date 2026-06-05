package batch

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/dao/store"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process/single"
	"github.com/aura-studio/tango/internal/source"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const testMongoURI = "mongodb://localhost:27017"

// pingMongo checks if MongoDB is available with a short timeout.
func pingMongo(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	clientOpts := options.Client().
		ApplyURI(testMongoURI).
		SetServerSelectionTimeout(2 * time.Second).
		SetConnectTimeout(2 * time.Second)

	client, err := mongo.Connect(clientOpts)
	if err != nil {
		t.Skipf("MongoDB not available at %s: %v", testMongoURI, err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(ctx)
		t.Skipf("MongoDB not available at %s: %v", testMongoURI, err)
	}
	_ = client.Disconnect(ctx)
}

// tester drives the single/batch uploaders against a throwaway database.
type tester struct {
	t   *testing.T
	dao *dao.Dao
	p   *parser.Parser
	db  *mongo.Database
}

// single ingests lines via the single (per-line immediate write) strategy.
func (tt *tester) single(lines ...string) {
	tt.t.Helper()
	up := single.NewUploader(tt.dao.Store, tt.p, nil)
	if err := up.Run(context.Background(), source.NewLines(lines)); err != nil {
		tt.t.Fatalf("single upload: %v", err)
	}
}

// batch ingests lines via the batch (accumulate + bulk flush) strategy.
func (tt *tester) batch(lines []string) {
	tt.t.Helper()
	up := NewUploader(tt.dao.Store, tt.p, 1000, nil)
	if err := up.Run(context.Background(), source.NewLines(lines)); err != nil {
		tt.t.Fatalf("batch upload: %v", err)
	}
}

func testSetup(t *testing.T) (*tester, func()) {
	t.Helper()
	pingMongo(t)

	dbName := fmt.Sprintf("tango_ingest_test_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
	uri := testMongoURI + "/" + dbName

	daoCfg := &dao.Config{
		Mongo: &daomongo.Config{URI: uri},
		Store: &store.Config{MaxElapsedTime: 5 * time.Second},
	}

	ctx := context.Background()

	da, err := dao.New(ctx, daoCfg)
	if err != nil {
		t.Fatalf("create dao: %v", err)
	}
	p, err := (&parser.Config{}).Build()
	if err != nil {
		t.Fatalf("build parser: %v", err)
	}

	if err := da.Store.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// Get DB handle for verification.
	client, _ := mongo.Connect(options.Client().ApplyURI(uri))
	db := client.Database(dbName)

	tt := &tester{t: t, dao: da, p: p, db: db}
	cleanup := func() {
		dropCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = db.Drop(dropCtx)
		_ = client.Disconnect(dropCtx)
		_ = da.Mongo.Close()
	}

	return tt, cleanup
}
