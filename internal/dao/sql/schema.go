package sql

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const schemaCollection = "_table_schemas"

// ColumnDef describes a single column's metadata.
type ColumnDef struct {
	Name          string `bson:"name"`
	Type          string `bson:"type"`            // e.g. "BIGINT", "VARCHAR", "DATETIME"
	NotNull       bool   `bson:"not_null"`        // NOT NULL constraint
	AutoIncrement bool   `bson:"auto_increment"`  // AUTO_INCREMENT
	HasDefault    bool   `bson:"has_default"`     // whether a DEFAULT clause was specified
	DefaultValue  string `bson:"default_value"`   // literal default value (e.g. "'USD'", "CURRENT_TIMESTAMP")
	DefaultIsFunc bool   `bson:"default_is_func"` // true if default is a function like CURRENT_TIMESTAMP
	OnUpdate      string `bson:"on_update"`       // "CURRENT_TIMESTAMP" or ""
}

// IndexDef describes an index.
type IndexDef struct {
	Name    string   `bson:"name"`
	Columns []string `bson:"columns"`
	Primary bool     `bson:"primary"`
	Unique  bool     `bson:"unique"`
}

// TableSchema represents the full schema of a table.
type TableSchema struct {
	TableName string      `bson:"_id"` // use table name as _id
	Columns   []ColumnDef `bson:"columns"`
	Indexes   []IndexDef  `bson:"indexes"`
}

// SchemaStore manages table schemas persisted in MongoDB.
type SchemaStore struct {
	mu    sync.RWMutex
	cache map[string]*TableSchema // db.table -> schema
}

func newSchemaStore() *SchemaStore {
	return &SchemaStore{cache: make(map[string]*TableSchema)}
}

func cacheKey(db, table string) string { return db + "." + table }

// Save persists a table schema to MongoDB and caches it.
func (ss *SchemaStore) Save(ctx context.Context, db *mongo.Database, schema *TableSchema) error {
	coll := db.Collection(schemaCollection)
	filter := bson.M{"_id": schema.TableName}
	update := bson.M{"$set": schema}
	opts := options.UpdateOne().SetUpsert(true)
	if _, err := coll.UpdateOne(ctx, filter, update, opts); err != nil {
		return err
	}
	ss.mu.Lock()
	ss.cache[cacheKey(db.Name(), schema.TableName)] = schema
	ss.mu.Unlock()
	return nil
}

// Get retrieves a table schema, first from cache then from MongoDB.
func (ss *SchemaStore) Get(ctx context.Context, db *mongo.Database, table string) *TableSchema {
	key := cacheKey(db.Name(), table)
	ss.mu.RLock()
	if s, ok := ss.cache[key]; ok {
		ss.mu.RUnlock()
		return s
	}
	ss.mu.RUnlock()

	// Load from MongoDB.
	var s TableSchema
	err := db.Collection(schemaCollection).FindOne(ctx, bson.M{"_id": table}).Decode(&s)
	if err != nil {
		return nil
	}
	ss.mu.Lock()
	ss.cache[key] = &s
	ss.mu.Unlock()
	return &s
}

// Delete removes a table schema from both cache and MongoDB.
func (ss *SchemaStore) Delete(ctx context.Context, db *mongo.Database, table string) {
	key := cacheKey(db.Name(), table)
	ss.mu.Lock()
	delete(ss.cache, key)
	ss.mu.Unlock()
	_, _ = db.Collection(schemaCollection).DeleteOne(ctx, bson.M{"_id": table})
}

// NextAutoIncrement atomically increments and returns the next auto-increment
// value for the given table/column pair. Stored in a separate collection.
func NextAutoIncrement(ctx context.Context, db *mongo.Database, table string) (int64, error) {
	coll := db.Collection("_auto_increment")
	filter := bson.M{"_id": table}
	update := bson.M{"$inc": bson.M{"seq": int64(1)}}
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)
	var result struct {
		Seq int64 `bson:"seq"`
	}
	err := coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&result)
	if err != nil {
		return 0, err
	}
	return result.Seq, nil
}

// ApplyDefaults fills in default values and auto_increment for INSERT docs.
func ApplyDefaults(ctx context.Context, db *mongo.Database, schema *TableSchema, docs []bson.M) error {
	for i := range docs {
		for _, col := range schema.Columns {
			// Skip if the column was explicitly provided.
			if _, exists := docs[i][col.Name]; exists {
				continue
			}

			// AUTO_INCREMENT
			if col.AutoIncrement {
				val, err := NextAutoIncrement(ctx, db, schema.TableName)
				if err != nil {
					return err
				}
				docs[i][col.Name] = val
				continue
			}

			// DEFAULT value
			if col.HasDefault {
				docs[i][col.Name] = evalDefault(col)
				continue
			}
		}
	}
	return nil
}

// ApplyOnUpdate sets ON UPDATE CURRENT_TIMESTAMP fields in the update doc.
func ApplyOnUpdate(schema *TableSchema, update bson.M) {
	// Look for $set sub-doc.
	setDoc, ok := update["$set"].(bson.M)
	if !ok {
		return
	}
	now := time.Now().UTC()
	for _, col := range schema.Columns {
		if col.OnUpdate == "" {
			continue
		}
		// Only apply if column was not explicitly set by user.
		if _, exists := setDoc[col.Name]; exists {
			continue
		}
		setDoc[col.Name] = now
	}
}

// evalDefault evaluates a column default value to a Go value.
func evalDefault(col ColumnDef) interface{} {
	if col.DefaultIsFunc {
		switch col.DefaultValue {
		case "CURRENT_TIMESTAMP", "NOW":
			return time.Now().UTC()
		case "CURDATE":
			return time.Now().UTC().Format("2006-01-02")
		case "CURTIME":
			return time.Now().UTC().Format("15:04:05")
		case "UUID":
			return "" // Placeholder — UUID generation is in expr package
		}
		return nil
	}

	// Literal default — try to interpret the stored value.
	val := col.DefaultValue

	// NULL
	if val == "NULL" || val == "" {
		return nil
	}

	// Boolean
	if val == "TRUE" {
		return true
	}
	if val == "FALSE" {
		return false
	}

	return val
}
