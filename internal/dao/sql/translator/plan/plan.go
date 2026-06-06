// Package plan defines the semantic model of a parsed SQL statement.
//
// A plan sits between the raw SQL AST (vitess sqlparser) and the executable
// statement types (translator/stmt). Producing it once and consuming it from
// renderers keeps SQL semantics in one place.
package plan

import (
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"vitess.io/vitess/go/vt/sqlparser"
)

// SourceRef is the semantic representation of a FROM/JOIN source.
type SourceRef struct {
	Database   string
	Collection string
	Alias      string
	ExplicitAs bool
}

// FieldRef is the semantic representation of a projected or filtered field.
type FieldRef struct {
	SourceAlias string
	Parts       []string
}

// Path returns the qualified field path ("alias.field"), or just the
// unqualified path if no source alias is present.
func (f FieldRef) Path() string {
	path := strings.Join(f.Parts, ".")
	if f.SourceAlias == "" {
		return path
	}
	if path == "" {
		return f.SourceAlias
	}
	return f.SourceAlias + "." + path
}

// UnqualifiedPath returns just the dotted field path, without source alias.
func (f FieldRef) UnqualifiedPath() string {
	return strings.Join(f.Parts, ".")
}

// SelectItemKind classifies an entry in the SELECT list.
type SelectItemKind string

const (
	SelectItemField     SelectItemKind = "field"
	SelectItemAggregate SelectItemKind = "aggregate"
	SelectItemExpr      SelectItemKind = "expr" // arbitrary expression (a+b, UPPER(name), CASE...)
)

// AggFunc identifies a supported aggregate function.
type AggFunc string

const (
	AggFuncCount AggFunc = "COUNT"
	AggFuncSum   AggFunc = "SUM"
	AggFuncAvg   AggFunc = "AVG"
	AggFuncMin   AggFunc = "MIN"
	AggFuncMax   AggFunc = "MAX"
)

// AggSpec describes a single aggregate call.
type AggSpec struct {
	Func     AggFunc
	Arg      *FieldRef      // simple column argument
	ArgExpr  sqlparser.Expr // arbitrary expression argument (e.g. SUM(price*qty))
	Star     bool
	Distinct bool // COUNT(DISTINCT col), SUM(DISTINCT col), ...
}

// SelectItem is one entry in the SELECT list.
type SelectItem struct {
	Kind    SelectItemKind
	Field   *FieldRef
	Agg     *AggSpec
	Alias   string
	RawExpr sqlparser.Expr
}

// JoinPlan describes one JOIN against the main source.
type JoinPlan struct {
	Right      SourceRef
	LeftField  FieldRef       // for equi-join
	RightField FieldRef       // for equi-join
	OnExpr     sqlparser.Expr // non-nil for non-equi join (raw ON expression)
	Outer      bool
}

// SelectPlan is the semantic plan of a SELECT statement.
type SelectPlan struct {
	Raw            *sqlparser.Select
	MainSource     SourceRef
	Joins          []JoinPlan
	Items          []SelectItem
	Filter         bson.M
	Sort           bson.D
	Limit          int64
	Offset         int64
	HasLimit       bool // true when an explicit LIMIT clause was present (distinguishes LIMIT 0 from no limit)
	Distinct       bool
	HasStar        bool
	HasAgg         bool
	UseAggregation bool
}
