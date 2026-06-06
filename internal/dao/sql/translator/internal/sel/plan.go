// Package sel translates parsed SELECT statements into executable
// statements via an intermediate plan.SelectPlan.
package sel

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/aura-studio/tango/internal/dao/sql/translator/internal/expr"
	"github.com/aura-studio/tango/internal/dao/sql/translator/plan"
	"github.com/aura-studio/tango/internal/dao/sql/translator/stmt"
)

// Translate plans the SELECT and renders it to a stmt.Statement.
func Translate(s *sqlparser.Select) (stmt.Statement, error) {
	p, err := Plan(s)
	if err != nil {
		return nil, err
	}
	return Render(p)
}

// Plan lifts a parsed SELECT into an internal plan.SelectPlan.
func Plan(s *sqlparser.Select) (*plan.SelectPlan, error) {
	if len(s.From) == 0 {
		return nil, fmt.Errorf("SELECT requires FROM clause")
	}

	mainSource, joins, err := extractFromPlan(s.From[0])
	if err != nil {
		return nil, err
	}

	items, hasStar, hasAgg, hasExpr, err := planSelectItems(s.SelectExprs)
	if err != nil {
		return nil, err
	}

	useAggregation := len(joins) > 0 || hasAgg || s.GroupBy != nil || s.Having != nil || hasExpr
	if !useAggregation && s.Distinct && len(items) > 1 {
		useAggregation = true
	}

	filter := bson.M{}
	if s.Where != nil {
		filter, err = expr.TranslateWhere(s.Where.Expr)
		if err != nil {
			return nil, err
		}
	}

	sort, err := buildSort(s.OrderBy)
	if err != nil {
		return nil, err
	}

	limit, offset, hasLimit, err := buildLimit(s.Limit)
	if err != nil {
		return nil, err
	}

	return &plan.SelectPlan{
		Raw:            s,
		MainSource:     mainSource,
		Joins:          joins,
		Items:          items,
		Filter:         filter,
		Sort:           sort,
		Limit:          limit,
		Offset:         offset,
		HasLimit:       hasLimit,
		Distinct:       s.Distinct,
		HasStar:        hasStar,
		HasAgg:         hasAgg,
		UseAggregation: useAggregation,
	}, nil
}

// Render turns a SelectPlan into an executable Statement.
func Render(p *plan.SelectPlan) (stmt.Statement, error) {
	if p.UseAggregation {
		return buildAggregate(p)
	}
	return buildFind(p)
}
