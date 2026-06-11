package sql

import (
	"fmt"

	mongosql "github.com/aura-studio/mongosql/driver"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Result is the outcome of a SQL statement. It mirrors mongosql's result shape
// with bson tags so it serializes as relaxed Extended JSON (SELECT rows carry
// native BSON types) — matching the gateway /ejson response convention.
// Pointer fields let omitempty distinguish "not set" (nil -> dropped) from a
// legitimate empty value that must still serialize — the same trade-off as
// ejson.Response: count fields keep a zero (e.g. nothing matched), and Rows
// keeps an empty array so a 0-row SELECT still emits "rows": [] rather than
// collapsing to {"kind": "select"}. InsertedIDs stays a plain slice — a
// successful INSERT carries at least one id.
type Result struct {
	Kind          string                    `bson:"kind"`                    // "select" | "insert" | "update" | "delete"
	Rows          *[]map[string]interface{} `bson:"rows,omitempty"`          // SELECT (always set, possibly empty)
	InsertedIDs   []interface{}             `bson:"insertedIds,omitempty"`   // INSERT
	MatchedCount  *int64                    `bson:"matchedCount,omitempty"`  // UPDATE (always set)
	ModifiedCount *int64                    `bson:"modifiedCount,omitempty"` // UPDATE (always set)
	DeletedCount  *int64                    `bson:"deletedCount,omitempty"`  // DELETE (always set)
}

// fromMongosql converts a mongosql driver result into tango's Result, setting
// the pointer fields owned by the statement kind so they serialize even when
// empty (zero rows, zero counts).
func fromMongosql(r *mongosql.Result) *Result {
	if r == nil {
		return nil
	}
	out := &Result{Kind: r.Kind, InsertedIDs: r.InsertedIDs}
	switch r.Kind {
	case "select":
		rows := r.Rows
		if rows == nil { // the driver returns a non-nil empty slice; keep the contract regardless
			rows = []map[string]interface{}{}
		}
		out.Rows = &rows
	case "update":
		matched, modified := r.MatchedCount, r.ModifiedCount
		out.MatchedCount = &matched
		out.ModifiedCount = &modified
	case "delete":
		deleted := r.DeletedCount
		out.DeletedCount = &deleted
	}
	return out
}

// MarshalEJSON encodes the result as relaxed Extended JSON.
func (r *Result) MarshalEJSON() ([]byte, error) {
	b, err := bson.MarshalExtJSON(r, false, false)
	if err != nil {
		return nil, fmt.Errorf("sql: encode result: %w", err)
	}
	return b, nil
}
