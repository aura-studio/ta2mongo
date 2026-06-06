package sel

import (
	"fmt"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/aura-studio/tango/internal/dao/sql/translator/plan"
)

func planSelectItems(exprs *sqlparser.SelectExprs) (items []plan.SelectItem, hasStar, hasAgg, hasExpr bool, err error) {
	if exprs == nil {
		return nil, true, false, false, nil
	}
	for _, se := range exprs.Exprs {
		switch v := se.(type) {
		case *sqlparser.StarExpr:
			hasStar = true
		case *sqlparser.AliasedExpr:
			item := plan.SelectItem{Alias: v.As.String(), RawExpr: v.Expr}
			if isAggExpr(v.Expr) {
				agg, perr := planAggSpec(v.Expr)
				if perr != nil {
					return nil, false, false, false, perr
				}
				item.Kind = plan.SelectItemAggregate
				item.Agg = agg
				hasAgg = true
			} else if field, ferr := fieldRefFromExpr(v.Expr); ferr == nil {
				// Pure column reference.
				item.Kind = plan.SelectItemField
				item.Field = &field
			} else {
				// Arbitrary expression (arithmetic, function, CASE, etc.).
				item.Kind = plan.SelectItemExpr
				hasExpr = true
			}
			items = append(items, item)
		default:
			return nil, false, false, false, fmt.Errorf("unsupported SELECT expr type: %T", se)
		}
	}
	return items, hasStar, hasAgg, hasExpr, nil
}

func isAggExpr(e sqlparser.Expr) bool {
	switch v := e.(type) {
	case *sqlparser.FuncExpr:
		switch strings.ToUpper(v.Name.String()) {
		case "COUNT", "SUM", "AVG", "MIN", "MAX":
			return true
		}
	case *sqlparser.CountStar, *sqlparser.Count, *sqlparser.Sum,
		*sqlparser.Avg, *sqlparser.Min, *sqlparser.Max:
		return true
	}
	return false
}

func planAggSpec(e sqlparser.Expr) (*plan.AggSpec, error) {
	switch v := e.(type) {
	case *sqlparser.CountStar:
		return &plan.AggSpec{Func: plan.AggFuncCount, Star: true}, nil
	case *sqlparser.Count:
		if len(v.Args) == 0 {
			return &plan.AggSpec{Func: plan.AggFuncCount, Star: true}, nil
		}
		// Allow expression in COUNT (not just column).
		field, err := fieldRefFromExpr(v.Args[0])
		if err != nil {
			// Expression argument for COUNT — store raw expr.
			return &plan.AggSpec{Func: plan.AggFuncCount, ArgExpr: v.Args[0], Distinct: v.Distinct}, nil
		}
		return &plan.AggSpec{Func: plan.AggFuncCount, Arg: &field, Distinct: v.Distinct}, nil
	case *sqlparser.Sum:
		return planUnaryAgg(plan.AggFuncSum, v.Arg, v.Distinct, "SUM")
	case *sqlparser.Avg:
		return planUnaryAgg(plan.AggFuncAvg, v.Arg, v.Distinct, "AVG")
	case *sqlparser.Min:
		return planUnaryAgg(plan.AggFuncMin, v.Arg, v.Distinct, "MIN")
	case *sqlparser.Max:
		return planUnaryAgg(plan.AggFuncMax, v.Arg, v.Distinct, "MAX")
	case *sqlparser.FuncExpr:
		name := plan.AggFunc(strings.ToUpper(v.Name.String()))
		if len(v.Exprs) == 0 {
			return &plan.AggSpec{Func: name, Star: true}, nil
		}
		field, err := fieldRefFromExpr(v.Exprs[0])
		if err != nil {
			// Expression argument.
			return &plan.AggSpec{Func: name, ArgExpr: v.Exprs[0]}, nil
		}
		return &plan.AggSpec{Func: name, Arg: &field}, nil
	}
	return nil, fmt.Errorf("not an aggregate expression: %T", e)
}

func planUnaryAgg(fn plan.AggFunc, e sqlparser.Expr, distinct bool, name string) (*plan.AggSpec, error) {
	field, err := fieldRefFromExpr(e)
	if err != nil {
		// Expression argument (e.g. SUM(price * qty)).
		return &plan.AggSpec{Func: fn, ArgExpr: e, Distinct: distinct}, nil
	}
	return &plan.AggSpec{Func: fn, Arg: &field, Distinct: distinct}, nil
}
