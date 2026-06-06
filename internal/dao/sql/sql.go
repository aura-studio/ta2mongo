// Package sql is the shared functional core of Tango's SQL Data API: a thin
// SQL→MongoDB layer that parses SQL (vitess), translates it to a MongoDB
// statement, and executes it against an injected MongoDB connection. It is a dao
// subpackage fronted by the dao root package (see dao.go); other domains reach it
// through dao, never importing dao/sql directly. The code is vendored (copied)
// from github.com/aura-studio/mongosql (driver + translator) and adapted to take
// a tango dao/mongo.MongoResource instead of dialing its own connection.
package sql

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/dao/sql/translator"
	"github.com/aura-studio/tango/internal/dao/sql/translator/stmt"
)

// Driver executes SQL statements against a MongoDB database.
type Driver struct {
	client  *mongo.Client
	db      *mongo.Database
	tr      *translator.Translator
	Schemas *SchemaStore
}

// Result represents the outcome of executing a SQL statement. It is encoded back
// to callers as relaxed Extended JSON (see MarshalEJSON), since SELECT rows carry
// native BSON types.
type Result struct {
	Kind          string                   `bson:"kind"`                  // "select" | "insert" | "update" | "delete"
	Rows          []map[string]interface{} `bson:"rows,omitempty"`        // populated for SELECT
	InsertedIDs   []interface{}            `bson:"insertedIds,omitempty"` // populated for INSERT
	MatchedCount  int64                    `bson:"matchedCount,omitempty"`  // populated for UPDATE
	ModifiedCount int64                    `bson:"modifiedCount,omitempty"` // populated for UPDATE
	DeletedCount  int64                    `bson:"deletedCount,omitempty"`  // populated for DELETE
}

// New builds a SQL driver over an existing MongoDB resource. The connection is
// owned by the caller (tango's dao); the driver never dials or disconnects it.
func New(res *daomongo.MongoResource) (*Driver, error) {
	if res == nil || res.Client == nil || res.DB == nil {
		return nil, fmt.Errorf("sql: nil MongoDB resource")
	}
	tr, err := translator.New()
	if err != nil {
		return nil, err
	}
	return &Driver{
		client:  res.Client,
		db:      res.DB,
		tr:      tr,
		Schemas: newSchemaStore(),
	}, nil
}

// DB exposes the underlying *mongo.Database (useful for tests / setup).
func (d *Driver) DB() *mongo.Database { return d.db }

// UseDB switches the driver to operate on the named database.
func (d *Driver) UseDB(name string) {
	d.db = d.client.Database(name)
}

// Client exposes the underlying *mongo.Client.
func (d *Driver) Client() *mongo.Client { return d.client }

// Exec parses and executes the given SQL.
func (d *Driver) Exec(ctx context.Context, sql string) (*Result, error) {
	st, err := d.tr.Translate(sql)
	if err != nil {
		return nil, err
	}
	switch s := st.(type) {
	case *stmt.FindStmt:
		return d.execFind(ctx, s)
	case *stmt.AggregateStmt:
		return d.execAggregate(ctx, s)
	case *stmt.InsertStmt:
		return d.execInsert(ctx, s)
	case *stmt.UpdateStmt:
		return d.execUpdate(ctx, s)
	case *stmt.DeleteStmt:
		return d.execDelete(ctx, s)
	case *stmt.InsertSelectStmt:
		return d.execInsertSelect(ctx, s)
	}
	return nil, fmt.Errorf("unknown statement: %T", st)
}

func (d *Driver) execFind(ctx context.Context, s *stmt.FindStmt) (*Result, error) {
	coll := d.db.Collection(s.Collection)

	// Statically-empty result (e.g. LIMIT 0): return no rows without querying.
	if s.Empty {
		return &Result{Kind: "select", Rows: []map[string]interface{}{}}, nil
	}

	if s.Distinct != "" {
		dr := coll.Distinct(ctx, s.Distinct, s.Filter)
		if err := dr.Err(); err != nil {
			return nil, err
		}
		var values []interface{}
		if err := dr.Decode(&values); err != nil {
			return nil, err
		}
		rows := make([]map[string]interface{}, 0, len(values))
		for _, v := range values {
			rows = append(rows, map[string]interface{}{s.Distinct: v})
		}
		return &Result{Kind: "select", Rows: rows}, nil
	}

	opts := options.Find()
	if s.Projection != nil {
		opts.SetProjection(s.Projection)
	}
	if s.Sort != nil {
		opts.SetSort(s.Sort)
	}
	if s.Limit > 0 {
		opts.SetLimit(s.Limit)
	}
	if s.Skip > 0 {
		opts.SetSkip(s.Skip)
	}

	cur, err := coll.Find(ctx, s.Filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	rows, err := drainCursor(ctx, cur)
	if err != nil {
		return nil, err
	}
	return &Result{Kind: "select", Rows: rows}, nil
}

func (d *Driver) execAggregate(ctx context.Context, s *stmt.AggregateStmt) (*Result, error) {
	// Statically-empty result (e.g. LIMIT 0): return no rows without querying.
	if s.Empty {
		return &Result{Kind: "select", Rows: []map[string]interface{}{}}, nil
	}
	coll := d.db.Collection(s.Collection)
	cur, err := coll.Aggregate(ctx, s.Pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	rows, err := drainCursor(ctx, cur)
	if err != nil {
		return nil, err
	}
	return &Result{Kind: "select", Rows: rows}, nil
}

func (d *Driver) execInsert(ctx context.Context, s *stmt.InsertStmt) (*Result, error) {
	coll := d.db.Collection(s.Collection)

	// Apply schema defaults (AUTO_INCREMENT, DEFAULT values).
	if schema := d.Schemas.Get(ctx, d.db, s.Collection); schema != nil {
		if err := ApplyDefaults(ctx, d.db, schema, s.Docs); err != nil {
			return nil, fmt.Errorf("apply defaults: %w", err)
		}
	}

	docs := make([]interface{}, len(s.Docs))
	for i, x := range s.Docs {
		docs[i] = x
	}
	res, err := coll.InsertMany(ctx, docs)
	if err != nil {
		return nil, err
	}
	return &Result{Kind: "insert", InsertedIDs: res.InsertedIDs}, nil
}

func (d *Driver) execUpdate(ctx context.Context, s *stmt.UpdateStmt) (*Result, error) {
	coll := d.db.Collection(s.Collection)

	// Apply ON UPDATE CURRENT_TIMESTAMP.
	if schema := d.Schemas.Get(ctx, d.db, s.Collection); schema != nil {
		ApplyOnUpdate(schema, s.Update)
	}

	// The translator marks pipeline-style updates with a synthetic
	// "$pipeline": true key in the update document. When present, we
	// strip it and pass the remainder as a single-stage aggregation
	// pipeline (required by MongoDB to evaluate $-expressions in $set).
	var (
		res *mongo.UpdateResult
		err error
	)
	if _, isPipeline := s.Update["$pipeline"]; isPipeline {
		// Re-construct a clean $set stage with the aggregation expressions
		// the translator stored in s.Update["$set"].
		setDoc, _ := s.Update["$set"].(bson.M)
		pipeline := mongo.Pipeline{{{Key: "$set", Value: setDoc}}}
		res, err = coll.UpdateMany(ctx, s.Filter, pipeline)
	} else {
		res, err = coll.UpdateMany(ctx, s.Filter, s.Update)
	}
	if err != nil {
		return nil, err
	}
	return &Result{
		Kind:          "update",
		MatchedCount:  res.MatchedCount,
		ModifiedCount: res.ModifiedCount,
	}, nil
}

func (d *Driver) execDelete(ctx context.Context, s *stmt.DeleteStmt) (*Result, error) {
	coll := d.db.Collection(s.Collection)
	res, err := coll.DeleteMany(ctx, s.Filter)
	if err != nil {
		return nil, err
	}
	return &Result{Kind: "delete", DeletedCount: res.DeletedCount}, nil
}

func (d *Driver) execInsertSelect(ctx context.Context, s *stmt.InsertSelectStmt) (*Result, error) {
	// Run the SELECT pipeline, then insert results into the target collection.
	srcColl := d.db.Collection(s.SourceCollection)

	cur, err := srcColl.Aggregate(ctx, s.Pipeline)
	if err != nil {
		return nil, fmt.Errorf("INSERT ... SELECT aggregate: %w", err)
	}
	defer cur.Close(ctx)

	// Determine target collection (possibly in a different database).
	targetDB := d.db
	if s.TargetDatabase != "" {
		targetDB = d.client.Database(s.TargetDatabase)
	}
	targetColl := targetDB.Collection(s.TargetCollection)

	// Collect and remap rows.
	var docs []interface{}
	for cur.Next(ctx) {
		var raw bson.M
		if err := cur.Decode(&raw); err != nil {
			return nil, err
		}
		// Map SELECT output fields to INSERT column names.
		// The SELECT pipeline produces fields by their projected name/alias.
		// We need to map those to the INSERT column order.
		doc := bson.M{}
		i := 0
		for _, col := range s.Columns {
			// Try to find the column value in the raw result.
			if v, ok := raw[col]; ok {
				doc[col] = v
			} else {
				// Fall back to positional mapping: iterate raw map in order.
				// Since Go maps are unordered, we use the ordered BSON decode.
				// For simplicity, just assign whatever is available.
				if i < len(raw) {
					for k, v := range raw {
						if k != "_id" {
							doc[col] = v
							delete(raw, k)
							break
						}
					}
				}
			}
			i++
		}
		docs = append(docs, doc)
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return &Result{Kind: "insert", InsertedIDs: []interface{}{}}, nil
	}

	res, err := targetColl.InsertMany(ctx, docs)
	if err != nil {
		return nil, err
	}
	return &Result{Kind: "insert", InsertedIDs: res.InsertedIDs}, nil
}

func drainCursor(ctx context.Context, cur *mongo.Cursor) ([]map[string]interface{}, error) {
	rows := []map[string]interface{}{}
	for cur.Next(ctx) {
		var doc bson.M
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		rows = append(rows, map[string]interface{}(doc))
	}
	return rows, cur.Err()
}
