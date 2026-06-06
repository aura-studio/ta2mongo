package sel

import (
	"fmt"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/aura-studio/tango/internal/dao/sql/translator/plan"
)

// fieldRefFromExpr expects a column-only expression and returns its FieldRef.
func fieldRefFromExpr(e sqlparser.Expr) (plan.FieldRef, error) {
	cn, ok := e.(*sqlparser.ColName)
	if !ok {
		return plan.FieldRef{}, fmt.Errorf("unsupported select expression: %T", e)
	}
	return fieldRefFromColName(cn), nil
}

func fieldRefFromColName(c *sqlparser.ColName) plan.FieldRef {
	field := plan.FieldRef{Parts: []string{c.Name.String()}}
	if !c.Qualifier.IsEmpty() {
		field.SourceAlias = c.Qualifier.Name.String()
	}
	return field
}

func extractFromPlan(te sqlparser.TableExpr) (plan.SourceRef, []plan.JoinPlan, error) {
	switch v := te.(type) {
	case *sqlparser.AliasedTableExpr:
		source, err := sourceRefFromAliasedTable(v)
		if err != nil {
			return plan.SourceRef{}, nil, err
		}
		return source, nil, nil
	case *sqlparser.JoinTableExpr:
		left, leftJoins, err := extractFromPlan(v.LeftExpr)
		if err != nil {
			return plan.SourceRef{}, nil, err
		}
		rightAT, ok := v.RightExpr.(*sqlparser.AliasedTableExpr)
		if !ok {
			return plan.SourceRef{}, nil, fmt.Errorf("only simple JOIN of a table is supported, got %T", v.RightExpr)
		}
		rightSource, err := sourceRefFromAliasedTable(rightAT)
		if err != nil {
			return plan.SourceRef{}, nil, err
		}

		isLeft := v.Join == sqlparser.LeftJoinType || v.Join == sqlparser.NaturalLeftJoinType
		isRight := v.Join == sqlparser.RightJoinType || v.Join == sqlparser.NaturalRightJoinType

		// A RIGHT JOIN is rewritten as a LEFT JOIN with the two tables swapped:
		// "A RIGHT JOIN B ON c" == "B LEFT JOIN A ON c". The lookup model builds
		// the pipeline on the main (left) source, so the right table must become
		// the main source. This is only well-defined for a two-table join.
		if isRight {
			if len(leftJoins) > 0 {
				return plan.SourceRef{}, nil, fmt.Errorf("RIGHT JOIN with more than two tables is not supported")
			}
			newMain := rightSource
			joined := left
			if lf, rf, eqErr := extractJoinOnFields(v.Condition, newMain.Alias, joined.Alias); eqErr == nil {
				join := plan.JoinPlan{Right: joined, LeftField: lf, RightField: rf, Outer: true}
				return newMain, []plan.JoinPlan{join}, nil
			}
			if v.Condition == nil || v.Condition.On == nil {
				return plan.SourceRef{}, nil, fmt.Errorf("JOIN requires ON clause")
			}
			join := plan.JoinPlan{Right: joined, OnExpr: v.Condition.On, Outer: true}
			return newMain, []plan.JoinPlan{join}, nil
		}

		outer := isLeft

		// Try equi-join first (single equality on two columns).
		leftField, rightField, eqErr := extractJoinOnFields(v.Condition, left.Alias, rightSource.Alias)
		if eqErr == nil {
			join := plan.JoinPlan{
				Right:      rightSource,
				LeftField:  leftField,
				RightField: rightField,
				Outer:      outer,
			}
			return left, append(leftJoins, join), nil
		}

		// Non-equi join: store the raw ON expression for pipeline-style $lookup.
		if v.Condition == nil || v.Condition.On == nil {
			return plan.SourceRef{}, nil, fmt.Errorf("JOIN requires ON clause")
		}
		join := plan.JoinPlan{
			Right:  rightSource,
			OnExpr: v.Condition.On,
			Outer:  outer,
		}
		return left, append(leftJoins, join), nil
	}
	return plan.SourceRef{}, nil, fmt.Errorf("unsupported FROM clause: %T", te)
}

// SourceRefFromAliasedTable resolves a single AliasedTableExpr.
func SourceRefFromAliasedTable(t *sqlparser.AliasedTableExpr) (plan.SourceRef, error) {
	return sourceRefFromAliasedTable(t)
}

func sourceRefFromAliasedTable(t *sqlparser.AliasedTableExpr) (plan.SourceRef, error) {
	tn, ok := t.Expr.(sqlparser.TableName)
	if !ok {
		return plan.SourceRef{}, fmt.Errorf("subqueries in FROM not supported")
	}
	source := plan.SourceRef{
		Database:   tn.Qualifier.String(),
		Collection: tn.Name.String(),
		Alias:      tn.Name.String(),
		ExplicitAs: !t.As.IsEmpty(),
	}
	if !t.As.IsEmpty() {
		source.Alias = t.As.String()
	}
	return source, nil
}

func extractJoinOnFields(cond *sqlparser.JoinCondition, leftAlias, rightAlias string) (plan.FieldRef, plan.FieldRef, error) {
	if cond == nil || cond.On == nil {
		return plan.FieldRef{}, plan.FieldRef{}, fmt.Errorf("JOIN requires ON clause")
	}
	cmp, ok := cond.On.(*sqlparser.ComparisonExpr)
	if !ok || cmp.Operator != sqlparser.EqualOp {
		return plan.FieldRef{}, plan.FieldRef{}, fmt.Errorf("not an equi-join")
	}
	lc, lok := cmp.Left.(*sqlparser.ColName)
	rc, rok := cmp.Right.(*sqlparser.ColName)
	if !lok || !rok {
		return plan.FieldRef{}, plan.FieldRef{}, fmt.Errorf("JOIN ON must compare two columns for equi-join")
	}

	leftField := fieldRefFromColName(lc)
	rightField := fieldRefFromColName(rc)

	lq := ""
	if !lc.Qualifier.IsEmpty() {
		lq = lc.Qualifier.Name.String()
	}
	rq := ""
	if !rc.Qualifier.IsEmpty() {
		rq = rc.Qualifier.Name.String()
	}

	switch {
	case lq == leftAlias && rq == rightAlias:
		return leftField, rightField, nil
	case rq == leftAlias && lq == rightAlias:
		return rightField, leftField, nil
	case lq == "" && rq == rightAlias:
		return leftField, rightField, nil
	case rq == "" && lq == rightAlias:
		return rightField, leftField, nil
	}
	return leftField, rightField, nil
}
