// Aggregation-expression helpers.
//
// This file translates SQL value expressions into MongoDB aggregation
// expressions ($expr / pipeline-style updates / $project expressions).
package expr

import (
	"fmt"
	"strings"

	"vitess.io/vitess/go/vt/sqlparser"
)

// HasColumnRef reports whether the expression tree references any column.
func HasColumnRef(e sqlparser.Expr) bool {
	if e == nil {
		return false
	}
	found := false
	var walk func(sqlparser.Expr)
	walk = func(x sqlparser.Expr) {
		if found || x == nil {
			return
		}
		switch v := x.(type) {
		case *sqlparser.ColName:
			found = true
		case *sqlparser.BinaryExpr:
			walk(v.Left)
			walk(v.Right)
		case *sqlparser.UnaryExpr:
			walk(v.Expr)
		case *sqlparser.ComparisonExpr:
			walk(v.Left)
			walk(v.Right)
		case *sqlparser.AndExpr:
			walk(v.Left)
			walk(v.Right)
		case *sqlparser.OrExpr:
			walk(v.Left)
			walk(v.Right)
		case *sqlparser.NotExpr:
			walk(v.Expr)
		case *sqlparser.IsExpr:
			walk(v.Left)
		case *sqlparser.BetweenExpr:
			walk(v.Left)
			walk(v.From)
			walk(v.To)
		case sqlparser.ValTuple:
			for _, it := range v {
				walk(it)
			}
		case *sqlparser.FuncExpr:
			for _, a := range v.Exprs {
				walk(a)
			}
		case *sqlparser.SubstrExpr:
			walk(v.Name)
			walk(v.From)
			walk(v.To)
		case *sqlparser.CaseExpr:
			if v.Expr != nil {
				walk(v.Expr)
			}
			for _, w := range v.Whens {
				walk(w.Cond)
				walk(w.Val)
			}
			if v.Else != nil {
				walk(v.Else)
			}
		case *sqlparser.ConvertExpr:
			walk(v.Expr)
		}
	}
	walk(e)
	return found
}

// ToAggExpr lowers a SQL value expression into a MongoDB aggregation
// expression. The result can be embedded inside $expr (filters) or inside
// a pipeline-style $set / $project.
func ToAggExpr(e sqlparser.Expr) (interface{}, error) {
	return toAggExprInner(e, "")
}

// ToAggExprWithMain is like ToAggExpr but strips the main source alias
// from qualified column names so fields resolve to root level in pipelines.
func ToAggExprWithMain(e sqlparser.Expr, mainAlias string) (interface{}, error) {
	return toAggExprInner(e, mainAlias)
}

func toAggExprInner(e sqlparser.Expr, mainAlias string) (interface{}, error) {
	if e == nil {
		return nil, nil
	}
	switch v := e.(type) {
	case *sqlparser.ColName:
		return "$" + colNameWithMain(v, mainAlias), nil

	case *sqlparser.Literal:
		return LiteralValue(v)

	case sqlparser.BoolVal:
		return bool(v), nil

	case *sqlparser.NullVal:
		return nil, nil

	case *sqlparser.UnaryExpr:
		inner, err := toAggExprInner(v.Expr, mainAlias)
		if err != nil {
			return nil, err
		}
		switch v.Operator {
		case sqlparser.UPlusOp:
			return inner, nil
		case sqlparser.UMinusOp:
			return m("$multiply", arr(int64(-1), inner)), nil
		}
		return nil, fmt.Errorf("unsupported unary operator: %s", v.Operator.ToString())

	case *sqlparser.BinaryExpr:
		left, err := toAggExprInner(v.Left, mainAlias)
		if err != nil {
			return nil, err
		}
		right, err := toAggExprInner(v.Right, mainAlias)
		if err != nil {
			return nil, err
		}
		switch v.Operator {
		case sqlparser.PlusOp:
			return m("$add", arr(left, right)), nil
		case sqlparser.MinusOp:
			return m("$subtract", arr(left, right)), nil
		case sqlparser.MultOp:
			return m("$multiply", arr(left, right)), nil
		case sqlparser.DivOp:
			return m("$divide", arr(left, right)), nil
		case sqlparser.ModOp:
			return m("$mod", arr(left, right)), nil
		case sqlparser.IntDivOp:
			return m("$toLong", m("$divide", arr(left, right))), nil
		}
		return nil, fmt.Errorf("unsupported binary operator: %s", v.Operator.ToString())

	// ─── CASE WHEN → $switch / $cond ─────────────────────────────────────
	case *sqlparser.CaseExpr:
		return translateCaseExpr(v, mainAlias)

	// ─── Scalar functions over columns → aggregation operators ───────────
	case *sqlparser.FuncExpr:
		return translateFuncExpr(v, mainAlias)

	case *sqlparser.SubstrExpr:
		return translateSubstrExpr(v, mainAlias)

	case *sqlparser.ConvertExpr:
		// CAST / CONVERT — just pass through inner.
		return toAggExprInner(v.Expr, mainAlias)

	case *sqlparser.CurTimeFuncExpr:
		return evalCurTimeFunc(strings.ToUpper(v.Name.String())), nil

	// ─── Comparison inside agg expr (for CASE WHEN conditions) ───────────
	case *sqlparser.ComparisonExpr:
		return translateComparisonAgg(v, mainAlias)

	case *sqlparser.AndExpr:
		l, err := toAggExprInner(v.Left, mainAlias)
		if err != nil {
			return nil, err
		}
		r, err := toAggExprInner(v.Right, mainAlias)
		if err != nil {
			return nil, err
		}
		return m("$and", arr(l, r)), nil

	case *sqlparser.OrExpr:
		l, err := toAggExprInner(v.Left, mainAlias)
		if err != nil {
			return nil, err
		}
		r, err := toAggExprInner(v.Right, mainAlias)
		if err != nil {
			return nil, err
		}
		return m("$or", arr(l, r)), nil

	case *sqlparser.NotExpr:
		inner, err := toAggExprInner(v.Expr, mainAlias)
		if err != nil {
			return nil, err
		}
		return m("$not", arr(inner)), nil

	case *sqlparser.IsExpr:
		left, err := toAggExprInner(v.Left, mainAlias)
		if err != nil {
			return nil, err
		}
		switch v.Right {
		case sqlparser.IsNullOp:
			return m("$eq", arr(left, nil)), nil
		case sqlparser.IsNotNullOp:
			return m("$ne", arr(left, nil)), nil
		case sqlparser.IsTrueOp:
			return m("$eq", arr(left, true)), nil
		case sqlparser.IsNotTrueOp:
			return m("$ne", arr(left, true)), nil
		case sqlparser.IsFalseOp:
			return m("$eq", arr(left, false)), nil
		case sqlparser.IsNotFalseOp:
			return m("$ne", arr(left, false)), nil
		}
		return nil, fmt.Errorf("unsupported IS expression")

	case *sqlparser.BetweenExpr:
		left, err := toAggExprInner(v.Left, mainAlias)
		if err != nil {
			return nil, err
		}
		from, err := toAggExprInner(v.From, mainAlias)
		if err != nil {
			return nil, err
		}
		to, err := toAggExprInner(v.To, mainAlias)
		if err != nil {
			return nil, err
		}
		if v.IsBetween {
			return m("$and", arr(m("$gte", arr(left, from)), m("$lte", arr(left, to)))), nil
		}
		return m("$or", arr(m("$lt", arr(left, from)), m("$gt", arr(left, to)))), nil

	case sqlparser.ValTuple:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			val, err := toAggExprInner(item, mainAlias)
			if err != nil {
				return nil, err
			}
			out = append(out, val)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported expression in aggregation context: %T", e)
}

// translateCaseExpr converts CASE WHEN ... THEN ... ELSE ... END
// into MongoDB $switch or $cond.
func translateCaseExpr(c *sqlparser.CaseExpr, mainAlias string) (interface{}, error) {
	if c.Expr != nil {
		// Simple CASE: CASE expr WHEN val1 THEN res1 ... END
		// Convert to searched CASE: CASE WHEN expr=val1 THEN res1 ...
		base, err := toAggExprInner(c.Expr, mainAlias)
		if err != nil {
			return nil, err
		}
		branches := make([]interface{}, 0, len(c.Whens))
		for _, w := range c.Whens {
			cond, err := toAggExprInner(w.Cond, mainAlias)
			if err != nil {
				return nil, err
			}
			then, err := toAggExprInner(w.Val, mainAlias)
			if err != nil {
				return nil, err
			}
			branches = append(branches, map[string]interface{}{
				"case": m("$eq", arr(base, cond)),
				"then": then,
			})
		}
		result := map[string]interface{}{
			"$switch": map[string]interface{}{
				"branches": branches,
			},
		}
		if c.Else != nil {
			elseVal, err := toAggExprInner(c.Else, mainAlias)
			if err != nil {
				return nil, err
			}
			result["$switch"].(map[string]interface{})["default"] = elseVal
		} else {
			result["$switch"].(map[string]interface{})["default"] = nil
		}
		return result, nil
	}

	// Searched CASE: CASE WHEN cond1 THEN res1 WHEN cond2 THEN res2 ELSE def END
	if len(c.Whens) == 1 && c.Else != nil {
		// Optimize to $cond for simple two-branch case.
		cond, err := toAggExprInner(c.Whens[0].Cond, mainAlias)
		if err != nil {
			return nil, err
		}
		then, err := toAggExprInner(c.Whens[0].Val, mainAlias)
		if err != nil {
			return nil, err
		}
		elseVal, err := toAggExprInner(c.Else, mainAlias)
		if err != nil {
			return nil, err
		}
		return m("$cond", arr(cond, then, elseVal)), nil
	}

	branches := make([]interface{}, 0, len(c.Whens))
	for _, w := range c.Whens {
		cond, err := toAggExprInner(w.Cond, mainAlias)
		if err != nil {
			return nil, err
		}
		then, err := toAggExprInner(w.Val, mainAlias)
		if err != nil {
			return nil, err
		}
		branches = append(branches, map[string]interface{}{
			"case": cond,
			"then": then,
		})
	}
	sw := map[string]interface{}{
		"branches": branches,
	}
	if c.Else != nil {
		elseVal, err := toAggExprInner(c.Else, mainAlias)
		if err != nil {
			return nil, err
		}
		sw["default"] = elseVal
	} else {
		sw["default"] = nil
	}
	return map[string]interface{}{"$switch": sw}, nil
}

// translateComparisonAgg converts a comparison expression inside agg context.
func translateComparisonAgg(c *sqlparser.ComparisonExpr, mainAlias string) (interface{}, error) {
	left, err := toAggExprInner(c.Left, mainAlias)
	if err != nil {
		return nil, err
	}
	right, err := toAggExprInner(c.Right, mainAlias)
	if err != nil {
		return nil, err
	}
	switch c.Operator {
	case sqlparser.EqualOp, sqlparser.NullSafeEqualOp:
		return m("$eq", arr(left, right)), nil
	case sqlparser.NotEqualOp:
		return m("$ne", arr(left, right)), nil
	case sqlparser.LessThanOp:
		return m("$lt", arr(left, right)), nil
	case sqlparser.LessEqualOp:
		return m("$lte", arr(left, right)), nil
	case sqlparser.GreaterThanOp:
		return m("$gt", arr(left, right)), nil
	case sqlparser.GreaterEqualOp:
		return m("$gte", arr(left, right)), nil
	case sqlparser.InOp:
		return m("$in", arr(left, right)), nil
	case sqlparser.NotInOp:
		return m("$not", arr(m("$in", arr(left, right)))), nil
	case sqlparser.LikeOp:
		// $regexMatch (case-insensitive, matching MySQL default collation)
		lit, ok := c.Right.(*sqlparser.Literal)
		if ok && lit.Type == sqlparser.StrVal {
			return m("$regexMatch", map[string]interface{}{
				"input": left, "regex": LikeToRegex(lit.Val), "options": "i",
			}), nil
		}
		return nil, fmt.Errorf("LIKE in expression requires string literal")
	case sqlparser.NotLikeOp:
		lit, ok := c.Right.(*sqlparser.Literal)
		if ok && lit.Type == sqlparser.StrVal {
			return m("$not", arr(m("$regexMatch", map[string]interface{}{
				"input": left, "regex": LikeToRegex(lit.Val), "options": "i",
			}))), nil
		}
		return nil, fmt.Errorf("NOT LIKE in expression requires string literal")
	}
	return nil, fmt.Errorf("unsupported comparison operator in agg expr: %s", c.Operator.ToString())
}

// translateFuncExpr converts a SQL scalar function into a MongoDB aggregation
// expression. For functions with no column references, it falls back to
// constant evaluation via Value().
func translateFuncExpr(f *sqlparser.FuncExpr, mainAlias string) (interface{}, error) {
	if !HasColumnRef(f) {
		return Value(f)
	}
	name := strings.ToUpper(f.Name.String())
	args := make([]interface{}, 0, len(f.Exprs))
	for _, ae := range f.Exprs {
		v, err := toAggExprInner(ae, mainAlias)
		if err != nil {
			return nil, fmt.Errorf("function %s: %w", name, err)
		}
		args = append(args, v)
	}

	switch name {
	// String functions
	case "UPPER", "UCASE":
		return m("$toUpper", args[0]), nil
	case "LOWER", "LCASE":
		return m("$toLower", args[0]), nil
	case "CONCAT":
		return m("$concat", args), nil
	case "CONCAT_WS":
		if len(args) < 2 {
			return nil, fmt.Errorf("CONCAT_WS requires at least 2 args")
		}
		// Build interspersed array: [arg1, sep, arg2, sep, ...]
		parts := make([]interface{}, 0, len(args)*2-1)
		for i, a := range args[1:] {
			if i > 0 {
				parts = append(parts, args[0])
			}
			parts = append(parts, a)
		}
		return m("$concat", parts), nil
	case "SUBSTRING", "SUBSTR", "MID":
		if len(args) < 2 {
			return nil, fmt.Errorf("SUBSTRING requires at least 2 args")
		}
		substrArgs := map[string]interface{}{
			"string": args[0],
			"start":  args[1],
		}
		if len(args) >= 3 {
			substrArgs["count"] = args[2]
		}
		return m("$substrCP", arr(args[0], m("$subtract", arr(args[1], int64(1))), func() interface{} {
			if len(args) >= 3 {
				return args[2]
			}
			return int64(1000000) // effectively to end of string
		}())), nil
	case "LENGTH", "CHAR_LENGTH", "CHARACTER_LENGTH":
		return m("$strLenCP", args[0]), nil
	case "TRIM":
		return m("$trim", map[string]interface{}{"input": args[0]}), nil
	case "LTRIM":
		return m("$ltrim", map[string]interface{}{"input": args[0]}), nil
	case "RTRIM":
		return m("$rtrim", map[string]interface{}{"input": args[0]}), nil
	case "REPLACE":
		if len(args) < 3 {
			return nil, fmt.Errorf("REPLACE requires 3 args")
		}
		return m("$replaceAll", map[string]interface{}{
			"input":       args[0],
			"find":        args[1],
			"replacement": args[2],
		}), nil
	case "LEFT":
		if len(args) < 2 {
			return nil, fmt.Errorf("LEFT requires 2 args")
		}
		return m("$substrCP", arr(args[0], int64(0), args[1])), nil
	case "RIGHT":
		if len(args) < 2 {
			return nil, fmt.Errorf("RIGHT requires 2 args")
		}
		return m("$substrCP", arr(args[0],
			m("$subtract", arr(m("$strLenCP", args[0]), args[1])),
			args[1])), nil
	case "REVERSE":
		// MongoDB doesn't have $reverse for strings; only for arrays.
		// Use $reduce over split characters.
		return m("$reduce", map[string]interface{}{
			"input":        m("$reverseArray", m("$split", arr(args[0], ""))),
			"initialValue": "",
			"in":           m("$concat", arr("$$value", "$$this")),
		}), nil

	// Numeric functions
	case "ABS":
		return m("$abs", args[0]), nil
	case "CEIL", "CEILING":
		return m("$ceil", args[0]), nil
	case "FLOOR":
		return m("$floor", args[0]), nil
	case "ROUND":
		x := args[0]
		var d interface{} = int64(0)
		if len(args) >= 2 {
			d = args[1]
		}
		// MySQL ROUND rounds half away from zero; MongoDB $round uses banker's
		// rounding. Emulate: sign(x) * floor(|x| * 10^d + 0.5) / 10^d.
		mult := m("$pow", arr(int64(10), d))
		scaled := m("$add", arr(m("$multiply", arr(m("$abs", x), mult)), 0.5))
		floored := m("$floor", scaled)
		sign := m("$cond", arr(m("$lt", arr(x, int64(0))), int64(-1), int64(1)))
		return m("$divide", arr(m("$multiply", arr(sign, floored)), mult)), nil
	case "MOD":
		if len(args) < 2 {
			return nil, fmt.Errorf("MOD requires 2 args")
		}
		return m("$mod", arr(args[0], args[1])), nil
	case "POW", "POWER":
		if len(args) < 2 {
			return nil, fmt.Errorf("POW requires 2 args")
		}
		return m("$pow", arr(args[0], args[1])), nil
	case "SQRT":
		return m("$sqrt", args[0]), nil
	case "LOG", "LN":
		return m("$ln", args[0]), nil
	case "LOG10":
		return m("$log10", args[0]), nil
	case "LOG2":
		return m("$log", arr(args[0], int64(2))), nil
	case "EXP":
		return m("$exp", args[0]), nil

	// Conditional
	case "IF":
		if len(args) < 3 {
			return nil, fmt.Errorf("IF requires 3 args")
		}
		return m("$cond", arr(args[0], args[1], args[2])), nil
	case "IFNULL":
		if len(args) < 2 {
			return nil, fmt.Errorf("IFNULL requires 2 args")
		}
		return m("$ifNull", arr(args[0], args[1])), nil
	case "COALESCE":
		return m("$ifNull", args), nil
	case "NULLIF":
		if len(args) < 2 {
			return nil, fmt.Errorf("NULLIF requires 2 args")
		}
		return m("$cond", arr(m("$eq", arr(args[0], args[1])), nil, args[0])), nil

	// Type conversions
	case "CAST", "CONVERT":
		if len(args) > 0 {
			return args[0], nil
		}
		return nil, nil

	// Date functions
	case "NOW", "CURRENT_TIMESTAMP", "SYSDATE":
		return "$$NOW", nil
	case "YEAR":
		return m("$year", args[0]), nil
	case "MONTH":
		return m("$month", args[0]), nil
	case "DAY", "DAYOFMONTH":
		return m("$dayOfMonth", args[0]), nil
	case "HOUR":
		return m("$hour", args[0]), nil
	case "MINUTE":
		return m("$minute", args[0]), nil
	case "SECOND":
		return m("$second", args[0]), nil

	// Conversion to string
	case "TO_STRING":
		return m("$toString", args[0]), nil
	case "TO_INT", "TO_INTEGER":
		return m("$toInt", args[0]), nil
	case "TO_DOUBLE", "TO_FLOAT":
		return m("$toDouble", args[0]), nil
	}

	return nil, fmt.Errorf("unsupported function in expression: %s", name)
}

// translateSubstrExpr handles the SUBSTRING(str, pos[, len]) AST node.
func translateSubstrExpr(s *sqlparser.SubstrExpr, mainAlias string) (interface{}, error) {
	str, err := toAggExprInner(s.Name, mainAlias)
	if err != nil {
		return nil, err
	}
	from, err := toAggExprInner(s.From, mainAlias)
	if err != nil {
		return nil, err
	}
	// MongoDB $substrCP is 0-indexed; SQL SUBSTRING is 1-indexed.
	startIdx := m("$subtract", arr(from, int64(1)))
	length := interface{}(int64(1000000))
	if s.To != nil {
		length, err = toAggExprInner(s.To, mainAlias)
		if err != nil {
			return nil, err
		}
	}
	return m("$substrCP", arr(str, startIdx, length)), nil
}

// colNameWithMain renders a column name, stripping the main alias.
func colNameWithMain(c *sqlparser.ColName, mainAlias string) string {
	name := c.Name.String()
	if c.Qualifier.IsEmpty() {
		return name
	}
	q := c.Qualifier.Name.String()
	if q == mainAlias && mainAlias != "" {
		return name
	}
	return q + "." + name
}

// ───── helpers for readable map / array construction ─────────────────────

func m(key string, val interface{}) map[string]interface{} {
	return map[string]interface{}{key: val}
}

func arr(vals ...interface{}) []interface{} {
	return vals
}

// ───── ValueOrAggExpr / evalConstExpr (unchanged logic) ─────────────────

func ValueOrAggExpr(e sqlparser.Expr) (val interface{}, needsAgg bool, err error) {
	if HasColumnRef(e) {
		v, err := ToAggExpr(e)
		if err != nil {
			return nil, false, err
		}
		return v, true, nil
	}
	v, err := evalConstExpr(e)
	if err != nil {
		return nil, false, err
	}
	return v, false, nil
}

func evalConstExpr(e sqlparser.Expr) (interface{}, error) {
	switch v := e.(type) {
	case *sqlparser.BinaryExpr:
		l, err := evalConstExpr(v.Left)
		if err != nil {
			return nil, err
		}
		r, err := evalConstExpr(v.Right)
		if err != nil {
			return nil, err
		}
		return applyArith(v.Operator, l, r)
	case *sqlparser.UnaryExpr:
		inner, err := evalConstExpr(v.Expr)
		if err != nil {
			return nil, err
		}
		switch v.Operator {
		case sqlparser.UPlusOp:
			return inner, nil
		case sqlparser.UMinusOp:
			return applyArith(sqlparser.MultOp, int64(-1), inner)
		}
		return nil, fmt.Errorf("unsupported unary operator: %s", v.Operator.ToString())
	}
	return Value(e)
}

func applyArith(op sqlparser.BinaryExprOperator, a, b interface{}) (interface{}, error) {
	_, aIsFloat := a.(float64)
	_, bIsFloat := b.(float64)
	if aIsFloat || bIsFloat || op == sqlparser.DivOp {
		af, err := toFloat64(a)
		if err != nil {
			return nil, fmt.Errorf("arith: %w", err)
		}
		bf, err := toFloat64(b)
		if err != nil {
			return nil, fmt.Errorf("arith: %w", err)
		}
		switch op {
		case sqlparser.PlusOp:
			return af + bf, nil
		case sqlparser.MinusOp:
			return af - bf, nil
		case sqlparser.MultOp:
			return af * bf, nil
		case sqlparser.DivOp:
			if bf == 0 {
				return nil, nil
			}
			return af / bf, nil
		case sqlparser.ModOp:
			if bf == 0 {
				return nil, nil
			}
			q := int64(af / bf)
			return af - float64(q)*bf, nil
		}
		return nil, fmt.Errorf("unsupported arith operator: %s", op.ToString())
	}
	ai, err := toInt64(a)
	if err != nil {
		return nil, fmt.Errorf("arith: %w", err)
	}
	bi, err := toInt64(b)
	if err != nil {
		return nil, fmt.Errorf("arith: %w", err)
	}
	switch op {
	case sqlparser.PlusOp:
		return ai + bi, nil
	case sqlparser.MinusOp:
		return ai - bi, nil
	case sqlparser.MultOp:
		return ai * bi, nil
	case sqlparser.IntDivOp:
		if bi == 0 {
			return nil, nil
		}
		return ai / bi, nil
	case sqlparser.ModOp:
		if bi == 0 {
			return nil, nil
		}
		return ai % bi, nil
	}
	return nil, fmt.Errorf("unsupported arith operator: %s", op.ToString())
}
