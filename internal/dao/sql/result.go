package sql

import (
	"fmt"

	mongosql "github.com/aura-studio/mongosql/driver"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Result is the outcome of a SQL statement. It mirrors mongosql's result shape
// with bson tags so it serializes as relaxed Extended JSON (SELECT rows carry
// native BSON types) — matching the gateway /ejson response convention.
type Result struct {
	Kind          string                   `bson:"kind"`                    // "select" | "insert" | "update" | "delete"
	Rows          []map[string]interface{} `bson:"rows,omitempty"`          // SELECT
	InsertedIDs   []interface{}            `bson:"insertedIds,omitempty"`   // INSERT
	MatchedCount  int64                    `bson:"matchedCount,omitempty"`  // UPDATE
	ModifiedCount int64                    `bson:"modifiedCount,omitempty"` // UPDATE
	DeletedCount  int64                    `bson:"deletedCount,omitempty"`  // DELETE
}

// fromMongosql converts a mongosql driver result into tango's Result.
func fromMongosql(r *mongosql.Result) *Result {
	if r == nil {
		return nil
	}
	return &Result{
		Kind:          r.Kind,
		Rows:          r.Rows,
		InsertedIDs:   r.InsertedIDs,
		MatchedCount:  r.MatchedCount,
		ModifiedCount: r.ModifiedCount,
		DeletedCount:  r.DeletedCount,
	}
}

// MarshalEJSON encodes the result as relaxed Extended JSON.
func (r *Result) MarshalEJSON() ([]byte, error) {
	b, err := bson.MarshalExtJSON(r, false, false)
	if err != nil {
		return nil, fmt.Errorf("sql: encode result: %w", err)
	}
	return b, nil
}
