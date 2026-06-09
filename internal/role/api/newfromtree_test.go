package api

// v1.5.1 increment test (doc/test2.md G4): api.NewFromTree slices the
// dao/process/parser/cfgsync branches and delegates to New, so an engine built
// from a config tree ingests identically to one built with typed New. The tree
// is built with cfgtree.New (a leaf package) to avoid the config -> role -> api
// import cycle. Reuses testMongoURI / spliceDB / pingMongo from the package's
// cfgsync integration test.

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/cfgtree"
)

func TestNewFromTree_G4_ApiEquivalent(t *testing.T) {
	pingMongo(t)

	dbName := fmt.Sprintf("tango_api_nft_%d_%d", time.Now().UnixNano(), rand.Intn(10000))
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

	tree := cfgtree.New(map[string]any{
		"dao":     map[string]any{"mongo": map[string]any{"uri": uri}},
		"process": map[string]any{"mode": "batch"},
	})

	eng, err := NewFromTree(context.Background(), tree)
	if err != nil {
		t.Fatalf("NewFromTree: %v", err)
	}
	defer eng.Close()

	ctx := context.Background()
	if err := eng.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	// Same fixed mix the repo-level suite uses: 4 track events, 1 user_set, 1
	// invalid -> 4 events, 1 user, 1 dead_letter.
	lines := []string{
		`{"#type":"track","#event_name":"e1","#time":"2026-06-09","#uuid":"g4-e1","#account_id":"acc"}`,
		`{"#type":"track","#event_name":"e2","#time":"2026-06-09","#uuid":"g4-e2","#account_id":"acc"}`,
		`{"#type":"track","#event_name":"e3","#time":"2026-06-09","#uuid":"g4-e3","#account_id":"acc"}`,
		`{"#type":"track","#event_name":"e4","#time":"2026-06-09","#uuid":"g4-e4","#account_id":"acc"}`,
		`{"#type":"user_set","#time":"2026-06-09","#uuid":"g4-u1","#account_id":"acc","properties":{"name":"A"}}`,
		"this is not json",
	}
	res, err := eng.Upload(ctx, lines)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if res.EventWrites != 4 || res.UserWrites != 1 || res.DeadLetters != 1 {
		t.Errorf("result = %+v, want EventWrites=4 UserWrites=1 DeadLetters=1", res)
	}

	count := func(coll string) int64 {
		n, err := db.Collection(coll).CountDocuments(ctx, bson.M{})
		if err != nil {
			t.Fatalf("count %s: %v", coll, err)
		}
		return n
	}
	if got := count("event"); got != 4 {
		t.Errorf("events = %d, want 4", got)
	}
	if got := count("user"); got != 1 {
		t.Errorf("users = %d, want 1", got)
	}
	if got := count("dead_letter"); got != 1 {
		t.Errorf("dead_letters = %d, want 1", got)
	}
}
