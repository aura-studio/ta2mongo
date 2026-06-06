// Package stmt defines the public, executable representation of a translated
// SQL statement. It is the API surface consumed by execution drivers.
package stmt

import "go.mongodb.org/mongo-driver/v2/bson"

// Statement is the translated, executable representation of a SQL statement.
type Statement interface {
	statementType() string
}

// FindStmt represents a simple SELECT that maps to collection.Find().
type FindStmt struct {
	Collection string
	Filter     bson.M
	Projection bson.M // nil if SELECT *
	Sort       bson.D
	Limit      int64 // 0 means no limit
	Skip       int64
	Distinct   string // non-empty for SELECT DISTINCT col FROM ...
	Empty      bool   // true for a statically-empty result set (e.g. LIMIT 0)
}

func (*FindStmt) statementType() string { return "find" }

// AggregateStmt represents queries that need an aggregation pipeline
// (GROUP BY, HAVING, JOIN, aggregate functions, etc.).
type AggregateStmt struct {
	Collection string
	Pipeline   []bson.M
	Empty      bool // true for a statically-empty result set (e.g. LIMIT 0)
}

func (*AggregateStmt) statementType() string { return "aggregate" }

// InsertStmt represents INSERT INTO ... VALUES (...).
type InsertStmt struct {
	Collection string
	Docs       []bson.M
}

func (*InsertStmt) statementType() string { return "insert" }

// UpdateStmt represents UPDATE ... SET ... WHERE ...
type UpdateStmt struct {
	Collection string
	Filter     bson.M
	Update     bson.M // already wrapped with $set
}

func (*UpdateStmt) statementType() string { return "update" }

// DeleteStmt represents DELETE FROM ... WHERE ...
type DeleteStmt struct {
	Collection string
	Filter     bson.M
}

func (*DeleteStmt) statementType() string { return "delete" }

// InsertSelectStmt represents INSERT INTO ... SELECT ... FROM ...
// The SELECT is compiled into an aggregation pipeline that ends with $merge.
type InsertSelectStmt struct {
	SourceCollection string   // collection to read from (FROM clause)
	SourceDatabase   string   // database of source, empty = current
	TargetCollection string   // collection to insert into
	TargetDatabase   string   // database of target, empty = current
	Pipeline         []bson.M // aggregation pipeline (without $merge)
	Columns          []string // INSERT column names
}

func (*InsertSelectStmt) statementType() string { return "insert_select" }
