// Package ejson is the shared functional core of Tango's Mongo Data API: a thin,
// fully-open passthrough to MongoDB CRUD/aggregate driven by a single
// Extended-JSON (EJSON) request shell with a Data-API-style action. It is a dao
// subpackage and is fronted by the dao root package (see dao.go); other domains
// reach it through dao, never importing dao/ejson directly.
//
// It is interface-agnostic — the api (Go method), gateway (HTTP POST /ejson) and
// cli (stdin) ends all call Execute with the same Request and get the same
// Response, exactly the way the upload path shares the api.Engine.
//
// Beyond CRUD/aggregate it also serves the admin schema view: listCollections
// enumerates a database's collections, sampleFields infers a collection's field
// set by sampling, listIndexes/createIndexes/dropIndexes read and manage a
// collection's indexes, and createTable/dropTable create and drop a collection
// (table) — all through the same request shell and Response.
//
// By design there are no restrictions: any database, any collection, any filter,
// operator, or aggregation pipeline is forwarded to the driver as-is, and no
// limit / return-count / timeout caps are imposed. Callers own access control.
package ejson

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
)

// Action identifiers accepted in Request.Action.
const (
	ActionFindOne   = "findOne"
	ActionFind      = "find"
	ActionInsertOne = "insertOne"
	ActionUpdateOne = "updateOne"
	ActionDeleteOne = "deleteOne"
	ActionAggregate = "aggregate"

	// Schema introspection & index management, backing the admin schema view.
	ActionListCollections = "listCollections" // database-level: list collection names
	ActionSampleFields    = "sampleFields"    // sample docs, union top-level fields + types
	ActionListIndexes     = "listIndexes"     // list a collection's indexes
	ActionCreateIndexes   = "createIndexes"   // create one index from an ordered key spec
	ActionDropIndexes     = "dropIndexes"     // drop one index by name
	ActionCreateTable     = "createTable"     // create a collection (table) by name
	ActionDropTable       = "dropTable"       // drop a collection (table) by name
)

// Request is the EJSON Data-API request shell. It is decoded from Extended JSON,
// so every BSON-bearing field (filter, document, update, pipeline, ...)
// round-trips native BSON types such as ObjectId and Date. Fields not relevant
// to the chosen action are ignored.
type Request struct {
	Action     string `bson:"action"`
	Database   string `bson:"database,omitempty"` // empty -> default DB from the connection URI
	Collection string `bson:"collection"`
	Filter     bson.M `bson:"filter,omitempty"`
	Projection bson.M `bson:"projection,omitempty"`
	Sort       bson.D `bson:"sort,omitempty"` // bson.D preserves key order
	Limit      int64  `bson:"limit,omitempty"`
	Skip       int64  `bson:"skip,omitempty"`
	Document   bson.M `bson:"document,omitempty"` // insertOne
	Update     bson.M `bson:"update,omitempty"`   // updateOne (forwarded as-is)
	Pipeline   bson.A `bson:"pipeline,omitempty"` // aggregate
	Upsert     bool   `bson:"upsert,omitempty"`
	// ---- schema introspection & index management ----
	Keys       bson.D `bson:"keys,omitempty"`       // createIndexes: ordered key spec, e.g. {"#event_name":1,"#time":1}
	IndexName  string `bson:"indexName,omitempty"`  // createIndexes (optional name) / dropIndexes (name to drop)
	Unique     bool   `bson:"unique,omitempty"`     // createIndexes
	SampleSize int64  `bson:"sampleSize,omitempty"` // sampleFields (default 200)
}

// Response carries the result of an action, encoded back as relaxed Extended
// JSON. Only the fields relevant to the action are populated. Pointer fields let
// omitempty distinguish "not set" (nil -> dropped) from a legitimate empty value
// that must still serialize: count fields keep a zero (e.g. nothing matched), and
// Documents keeps an empty array so find/aggregate always emit "documents": []
// rather than collapsing to {}.
type Response struct {
	Document      bson.M    `bson:"document,omitempty"`      // findOne
	Documents     *[]bson.M `bson:"documents,omitempty"`     // find / aggregate (always set, possibly empty)
	InsertedID    any       `bson:"insertedId,omitempty"`    // insertOne
	MatchedCount  *int64    `bson:"matchedCount,omitempty"`  // updateOne
	ModifiedCount *int64    `bson:"modifiedCount,omitempty"` // updateOne
	UpsertedID    any       `bson:"upsertedId,omitempty"`    // updateOne (upsert)
	DeletedCount  *int64    `bson:"deletedCount,omitempty"`  // deleteOne
	// ---- schema introspection & index management ----
	Collections *[]string `bson:"collections,omitempty"` // listCollections (always set, possibly empty)
	IndexName   string    `bson:"indexName,omitempty"`   // createIndexes (created) / dropIndexes (dropped)
	// listIndexes and sampleFields reuse Documents: listIndexes emits
	// [{name, unique, keys:[{field, dir}, ...]}], sampleFields [{field, types:[...]}].
}

// Execute dispatches req against the MongoDB resource. The target database is
// req.Database when set, otherwise the resource's default database (named in the
// connection URI). No validation beyond requiring a collection and a known
// action — everything else is forwarded to the driver.
func Execute(ctx context.Context, res *daomongo.MongoResource, req *Request) (*Response, error) {
	if req == nil {
		return nil, errors.New("ejson: nil request")
	}
	// Validate the request shell before touching the connection, so bad-request
	// errors (unknown action, missing collection/database) never depend on a live
	// connection.
	switch req.Action {
	case ActionFindOne, ActionFind, ActionInsertOne, ActionUpdateOne, ActionDeleteOne, ActionAggregate,
		ActionListCollections, ActionSampleFields, ActionListIndexes, ActionCreateIndexes, ActionDropIndexes,
		ActionCreateTable, ActionDropTable:
	case "":
		return nil, errors.New("ejson: action is required")
	default:
		return nil, fmt.Errorf("ejson: unknown action %q", req.Action)
	}
	// listCollections is database-level (no collection); every other action
	// operates on a named collection.
	if req.Action != ActionListCollections && req.Collection == "" {
		return nil, errors.New("ejson: collection is required")
	}
	dbName := req.Database
	if dbName == "" && res != nil && res.DB != nil {
		dbName = res.DB.Name()
	}
	if dbName == "" {
		return nil, errors.New("ejson: database is required (none in request or connection URI)")
	}
	if res == nil || res.Client == nil {
		return nil, errors.New("ejson: no MongoDB connection")
	}
	db := res.Client.Database(dbName)
	if req.Action == ActionListCollections {
		return listCollections(ctx, db)
	}
	coll := db.Collection(req.Collection)

	switch req.Action {
	case ActionFindOne:
		return findOne(ctx, coll, req)
	case ActionFind:
		return find(ctx, coll, req)
	case ActionInsertOne:
		return insertOne(ctx, coll, req)
	case ActionUpdateOne:
		return updateOne(ctx, coll, req)
	case ActionDeleteOne:
		return deleteOne(ctx, coll, req)
	case ActionAggregate:
		return aggregate(ctx, coll, req)
	case ActionSampleFields:
		return sampleFields(ctx, coll, req)
	case ActionListIndexes:
		return listIndexes(ctx, coll)
	case ActionCreateIndexes:
		return createIndexes(ctx, coll, req)
	case ActionDropIndexes:
		return dropIndexes(ctx, coll, req)
	case ActionCreateTable:
		return createTable(ctx, db, req.Collection)
	default: // ActionDropTable (only remaining validated action)
		return dropTable(ctx, coll)
	}
}

func findOne(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	opts := options.FindOne()
	if req.Projection != nil {
		opts.SetProjection(req.Projection)
	}
	if len(req.Sort) > 0 {
		opts.SetSort(req.Sort)
	}
	if req.Skip > 0 {
		opts.SetSkip(req.Skip)
	}
	var doc bson.M
	err := coll.FindOne(ctx, filterOrEmpty(req.Filter), opts).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return &Response{}, nil // no match -> empty response
	}
	if err != nil {
		return nil, err
	}
	return &Response{Document: doc}, nil
}

func find(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	opts := options.Find()
	if req.Projection != nil {
		opts.SetProjection(req.Projection)
	}
	if len(req.Sort) > 0 {
		opts.SetSort(req.Sort)
	}
	if req.Limit > 0 {
		opts.SetLimit(req.Limit)
	}
	if req.Skip > 0 {
		opts.SetSkip(req.Skip)
	}
	cur, err := coll.Find(ctx, filterOrEmpty(req.Filter), opts)
	if err != nil {
		return nil, err
	}
	return drain(ctx, cur)
}

func insertOne(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	if req.Document == nil {
		return nil, errors.New("ejson: insertOne requires a document")
	}
	res, err := coll.InsertOne(ctx, req.Document)
	if err != nil {
		return nil, err
	}
	return &Response{InsertedID: res.InsertedID}, nil
}

func updateOne(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	if req.Update == nil {
		return nil, errors.New("ejson: updateOne requires an update")
	}
	opts := options.UpdateOne()
	if req.Upsert {
		opts.SetUpsert(true)
	}
	res, err := coll.UpdateOne(ctx, filterOrEmpty(req.Filter), req.Update, opts)
	if err != nil {
		return nil, err
	}
	matched, modified := res.MatchedCount, res.ModifiedCount
	resp := &Response{MatchedCount: &matched, ModifiedCount: &modified}
	if res.UpsertedID != nil {
		resp.UpsertedID = res.UpsertedID
	}
	return resp, nil
}

func deleteOne(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	res, err := coll.DeleteOne(ctx, filterOrEmpty(req.Filter))
	if err != nil {
		return nil, err
	}
	deleted := res.DeletedCount
	return &Response{DeletedCount: &deleted}, nil
}

func aggregate(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	pipeline := req.Pipeline
	if pipeline == nil {
		pipeline = bson.A{}
	}
	cur, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	return drain(ctx, cur)
}

// drain reads a cursor fully into a Response.Documents slice. The slice is always
// non-nil (initialized to empty), and the field is a pointer, so even an empty
// result encodes as "documents": [] rather than being dropped by omitempty.
func drain(ctx context.Context, cur *mongo.Cursor) (*Response, error) {
	docs := []bson.M{}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return &Response{Documents: &docs}, nil
}

// filterOrEmpty returns an empty filter when none was supplied, matching the
// driver's expectation of a non-nil filter document.
func filterOrEmpty(f bson.M) bson.M {
	if f == nil {
		return bson.M{}
	}
	return f
}

// listCollections lists a database's collection names (sorted), so the admin
// schema view can enumerate a profile's tables.
func listCollections(ctx context.Context, db *mongo.Database) (*Response, error) {
	names, err := db.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	if names == nil {
		names = []string{}
	}
	return &Response{Collections: &names}, nil
}

// sampleFields infers a collection's field set by sampling up to SampleSize
// documents (default 200) and unioning their top-level keys, recording the BSON
// type(s) seen for each. MongoDB is schemaless, so this is best-effort: a field
// present in no sampled document does not appear. Fields are returned sorted
// (_id first), each as {field, types:[...]}, in Documents.
func sampleFields(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	size := req.SampleSize
	if size <= 0 {
		size = 200
	}
	cur, err := coll.Find(ctx, bson.D{}, options.Find().SetLimit(size))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	types := map[string]map[string]struct{}{}
	for cur.Next(ctx) {
		elems, err := cur.Current.Elements()
		if err != nil {
			continue // skip a malformed document rather than fail the whole sample
		}
		for _, e := range elems {
			k := e.Key()
			set := types[k]
			if set == nil {
				set = map[string]struct{}{}
				types[k] = set
			}
			set[bsonTypeName(e.Value().Type)] = struct{}{}
		}
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	fields := make([]string, 0, len(types))
	for k := range types {
		fields = append(fields, k)
	}
	sort.Slice(fields, func(i, j int) bool {
		if (fields[i] == "_id") != (fields[j] == "_id") {
			return fields[i] == "_id" // _id sorts first
		}
		return fields[i] < fields[j]
	})
	docs := make([]bson.M, 0, len(fields))
	for _, k := range fields {
		ts := make([]string, 0, len(types[k]))
		for t := range types[k] {
			ts = append(ts, t)
		}
		sort.Strings(ts)
		typesA := make(bson.A, len(ts))
		for i, t := range ts {
			typesA[i] = t
		}
		docs = append(docs, bson.M{"field": k, "types": typesA})
	}
	return &Response{Documents: &docs}, nil
}

// listIndexes returns a collection's indexes as Documents, each shaped
// {name, unique, keys:[{field, dir}, ...]} with key order preserved (compound
// index order is significant).
func listIndexes(ctx context.Context, coll *mongo.Collection) (*Response, error) {
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	docs := []bson.M{}
	for cur.Next(ctx) {
		var idx struct {
			Name   string `bson:"name"`
			Key    bson.D `bson:"key"`
			Unique bool   `bson:"unique"`
		}
		if err := cur.Decode(&idx); err != nil {
			return nil, err
		}
		keys := make(bson.A, 0, len(idx.Key))
		for _, kv := range idx.Key {
			keys = append(keys, bson.M{"field": kv.Key, "dir": indexDir(kv.Value)})
		}
		docs = append(docs, bson.M{"name": idx.Name, "unique": idx.Unique, "keys": keys})
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return &Response{Documents: &docs}, nil
}

// createIndexes creates a single index from an ordered key spec (Keys), with an
// optional name and a unique flag, returning the created index's name.
func createIndexes(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	if len(req.Keys) == 0 {
		return nil, errors.New("ejson: createIndexes requires a non-empty keys spec")
	}
	idxOpts := options.Index()
	if req.IndexName != "" {
		idxOpts.SetName(req.IndexName)
	}
	if req.Unique {
		idxOpts.SetUnique(true)
	}
	name, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: req.Keys, Options: idxOpts})
	if err != nil {
		return nil, err
	}
	return &Response{IndexName: name}, nil
}

// dropIndexes drops one index by name. Dropping _id_ is rejected by MongoDB
// itself; callers should also guard destructive drops at their own layer.
func dropIndexes(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	if req.IndexName == "" {
		return nil, errors.New("ejson: dropIndexes requires an index name")
	}
	if err := coll.Indexes().DropOne(ctx, req.IndexName); err != nil {
		return nil, err
	}
	return &Response{IndexName: req.IndexName}, nil
}

// createTable creates an (empty) collection by name. Creating a collection that
// already exists returns the driver's NamespaceExists error. The created name is
// echoed back in Collections.
func createTable(ctx context.Context, db *mongo.Database, name string) (*Response, error) {
	if err := db.CreateCollection(ctx, name); err != nil {
		return nil, err
	}
	return &Response{Collections: &[]string{name}}, nil
}

// dropTable drops a collection by name (all its documents and indexes go with
// it — destructive). Dropping a non-existent collection is a driver no-op. The
// dropped name is echoed back in Collections.
func dropTable(ctx context.Context, coll *mongo.Collection) (*Response, error) {
	name := coll.Name()
	if err := coll.Drop(ctx); err != nil {
		return nil, err
	}
	return &Response{Collections: &[]string{name}}, nil
}

// indexDir normalizes an index key direction: ±1 (and other numeric orders)
// become an int; a non-numeric direction (e.g. "text", "2dsphere") is kept as
// its original value.
func indexDir(v any) any {
	switch n := v.(type) {
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return v
	}
}

// bsonTypeName maps a BSON element type to a short human-friendly name for the
// schema view.
func bsonTypeName(t bson.Type) string {
	switch t {
	case bson.TypeString:
		return "string"
	case bson.TypeInt32:
		return "int"
	case bson.TypeInt64:
		return "long"
	case bson.TypeDouble:
		return "double"
	case bson.TypeDecimal128:
		return "decimal"
	case bson.TypeBoolean:
		return "bool"
	case bson.TypeDateTime:
		return "date"
	case bson.TypeTimestamp:
		return "timestamp"
	case bson.TypeObjectID:
		return "objectId"
	case bson.TypeEmbeddedDocument:
		return "object"
	case bson.TypeArray:
		return "array"
	case bson.TypeBinary:
		return "binary"
	case bson.TypeNull:
		return "null"
	default:
		return t.String()
	}
}
