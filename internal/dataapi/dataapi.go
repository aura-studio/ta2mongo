// Package dataapi is the shared functional core of Tango's Mongo Data API: a
// thin, fully-open passthrough to MongoDB CRUD/aggregate driven by a single
// Extended-JSON request shell with a Data-API-style action. It is interface-
// agnostic — the api (Go method), gateway (HTTP POST /data) and cli (stdin)
// ends all call Execute with the same Request and get the same Response, exactly
// the way the upload path shares the api.Engine.
//
// By design there are no restrictions: any database, any collection, any filter,
// operator, or aggregation pipeline is forwarded to the driver as-is, and no
// limit / return-count / timeout caps are imposed. Callers own access control.
package dataapi

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Action identifiers accepted in Request.Action.
const (
	ActionFindOne   = "findOne"
	ActionFind      = "find"
	ActionInsertOne = "insertOne"
	ActionUpdateOne = "updateOne"
	ActionDeleteOne = "deleteOne"
	ActionAggregate = "aggregate"
)

// Request is the Data-API request shell. It is decoded from Extended JSON, so
// every BSON-bearing field (filter, document, update, pipeline, ...) round-trips
// native BSON types such as ObjectId and Date. Fields not relevant to the chosen
// action are ignored.
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
}

// Response carries the result of an action, encoded back as relaxed Extended
// JSON. Only the fields relevant to the action are populated; count fields are
// pointers so a legitimate zero (e.g. nothing matched) still serializes instead
// of being dropped by omitempty.
type Response struct {
	Document      bson.M   `bson:"document,omitempty"`      // findOne
	Documents     []bson.M `bson:"documents,omitempty"`     // find / aggregate
	InsertedID    any      `bson:"insertedId,omitempty"`    // insertOne
	MatchedCount  *int64   `bson:"matchedCount,omitempty"`  // updateOne
	ModifiedCount *int64   `bson:"modifiedCount,omitempty"` // updateOne
	UpsertedID    any      `bson:"upsertedId,omitempty"`    // updateOne (upsert)
	DeletedCount  *int64   `bson:"deletedCount,omitempty"`  // deleteOne
}

// Execute dispatches req against the given client. The target database is
// req.Database when set, otherwise defaultDB (the database named in the
// connection URI). No validation beyond requiring a collection and a known
// action — everything else is forwarded to the driver.
func Execute(ctx context.Context, client *mongo.Client, defaultDB string, req *Request) (*Response, error) {
	if req == nil {
		return nil, errors.New("dataapi: nil request")
	}
	// Validate the request shell before touching the client, so bad-request errors
	// (unknown action, missing collection/database) never depend on a live
	// connection.
	switch req.Action {
	case ActionFindOne, ActionFind, ActionInsertOne, ActionUpdateOne, ActionDeleteOne, ActionAggregate:
	case "":
		return nil, errors.New("dataapi: action is required")
	default:
		return nil, fmt.Errorf("dataapi: unknown action %q", req.Action)
	}
	if req.Collection == "" {
		return nil, errors.New("dataapi: collection is required")
	}
	dbName := req.Database
	if dbName == "" {
		dbName = defaultDB
	}
	if dbName == "" {
		return nil, errors.New("dataapi: database is required (none in request or connection URI)")
	}
	coll := client.Database(dbName).Collection(req.Collection)

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
	default: // ActionAggregate (only remaining validated action)
		return aggregate(ctx, coll, req)
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
		return nil, errors.New("dataapi: insertOne requires a document")
	}
	res, err := coll.InsertOne(ctx, req.Document)
	if err != nil {
		return nil, err
	}
	return &Response{InsertedID: res.InsertedID}, nil
}

func updateOne(ctx context.Context, coll *mongo.Collection, req *Request) (*Response, error) {
	if req.Update == nil {
		return nil, errors.New("dataapi: updateOne requires an update")
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

// drain reads a cursor fully into a Response.Documents slice (never nil, so an
// empty result encodes as "documents": []).
func drain(ctx context.Context, cur *mongo.Cursor) (*Response, error) {
	docs := []bson.M{}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	return &Response{Documents: docs}, nil
}

// filterOrEmpty returns an empty filter when none was supplied, matching the
// driver's expectation of a non-nil filter document.
func filterOrEmpty(f bson.M) bson.M {
	if f == nil {
		return bson.M{}
	}
	return f
}
