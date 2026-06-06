// Package expr contains low-level conversions between vitess SQL expressions
// and MongoDB BSON values / filters. It is a leaf package: it does not depend
// on any other internal translator package.
package expr

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"vitess.io/vitess/go/vt/sqlparser"
)

// LiteralValue converts a parsed literal to a Go value.
func LiteralValue(l *sqlparser.Literal) (interface{}, error) {
	switch l.Type {
	case sqlparser.StrVal:
		return l.Val, nil
	case sqlparser.IntVal:
		if v, err := strconv.ParseInt(l.Val, 10, 64); err == nil {
			return v, nil
		}
		return l.Val, nil
	case sqlparser.FloatVal, sqlparser.DecimalVal:
		if v, err := strconv.ParseFloat(l.Val, 64); err == nil {
			return v, nil
		}
		return l.Val, nil
	case sqlparser.HexNum, sqlparser.HexVal, sqlparser.BitNum:
		return l.Val, nil
	case sqlparser.DateVal, sqlparser.TimeVal, sqlparser.TimestampVal:
		return l.Val, nil
	}
	return l.Val, nil
}

// Value extracts a Go scalar value from a value-producing expression.
// Used for the right-hand side of comparisons and INSERT/UPDATE values.
func Value(e sqlparser.Expr) (interface{}, error) {
	switch v := e.(type) {
	case *sqlparser.Literal:
		return LiteralValue(v)
	case sqlparser.BoolVal:
		return bool(v), nil
	case *sqlparser.NullVal:
		return nil, nil
	case sqlparser.ValTuple:
		out := make([]interface{}, 0, len(v))
		for _, item := range v {
			x, err := Value(item)
			if err != nil {
				return nil, err
			}
			out = append(out, x)
		}
		return out, nil
	case *sqlparser.UnaryExpr:
		if v.Operator == sqlparser.UMinusOp {
			inner, err := Value(v.Expr)
			if err != nil {
				return nil, err
			}
			switch n := inner.(type) {
			case int64:
				return -n, nil
			case float64:
				return -n, nil
			case string:
				return "-" + n, nil
			}
		}
	case *sqlparser.CurTimeFuncExpr:
		// NOW(), CURRENT_TIMESTAMP(), CURDATE(), CURTIME(), SYSDATE(), ...
		return evalCurTimeFunc(strings.ToUpper(v.Name.String())), nil
	case *sqlparser.FuncExpr:
		return evalFunc(v)
	case *sqlparser.SubstrExpr:
		s, err := Value(v.Name)
		if err != nil {
			return nil, fmt.Errorf("SUBSTRING: %w", err)
		}
		from, err := Value(v.From)
		if err != nil {
			return nil, fmt.Errorf("SUBSTRING: %w", err)
		}
		args := []interface{}{s, from}
		if v.To != nil {
			to, err := Value(v.To)
			if err != nil {
				return nil, fmt.Errorf("SUBSTRING: %w", err)
			}
			args = append(args, to)
		}
		return evalSubstring(args)
	case *sqlparser.ConvertExpr:
		// CAST(x AS T) / CONVERT(x, T) — return inner value as-is.
		return Value(v.Expr)
	case *sqlparser.ComparisonExpr:
		l, err := Value(v.Left)
		if err != nil {
			return nil, err
		}
		r, err := Value(v.Right)
		if err != nil {
			return nil, err
		}
		cmp := compareAny(l, r)
		switch v.Operator {
		case sqlparser.EqualOp, sqlparser.NullSafeEqualOp:
			return boolToInt(cmp == 0), nil
		case sqlparser.NotEqualOp:
			return boolToInt(cmp != 0), nil
		case sqlparser.LessThanOp:
			return boolToInt(cmp < 0), nil
		case sqlparser.LessEqualOp:
			return boolToInt(cmp <= 0), nil
		case sqlparser.GreaterThanOp:
			return boolToInt(cmp > 0), nil
		case sqlparser.GreaterEqualOp:
			return boolToInt(cmp >= 0), nil
		}
		return int64(0), nil
	}
	return nil, fmt.Errorf("unsupported value expression: %T", e)
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// isIntegerValue reports whether v is an integer-typed Go value.
func isIntegerValue(v interface{}) bool {
	switch v.(type) {
	case int, int32, int64:
		return true
	}
	return false
}

// evalSubstring implements MySQL SUBSTRING(str, pos[, len]) semantics.
func evalSubstring(args []interface{}) (interface{}, error) {
	s := toString(args[0])
	start, err := toInt64(args[1])
	if err != nil {
		return nil, fmt.Errorf("SUBSTRING start: %w", err)
	}
	if start > 0 {
		start--
	} else if start < 0 {
		start = int64(len(s)) + start
		if start < 0 {
			start = 0
		}
	}
	if start >= int64(len(s)) {
		return "", nil
	}
	end := int64(len(s))
	if len(args) >= 3 {
		length, err := toInt64(args[2])
		if err != nil {
			return nil, fmt.Errorf("SUBSTRING length: %w", err)
		}
		end = start + length
		if end > int64(len(s)) {
			end = int64(len(s))
		}
		if end < start {
			end = start
		}
	}
	return s[start:end], nil
}

// evalCurTimeFunc evaluates CURRENT_TIMESTAMP / NOW / CURDATE / CURTIME etc.
// to a Go time.Time or formatted string.
func evalCurTimeFunc(name string) interface{} {
	now := time.Now().UTC()
	switch name {
	case "NOW", "CURRENT_TIMESTAMP", "LOCALTIMESTAMP", "LOCALTIME", "SYSDATE":
		return now
	case "CURDATE", "CURRENT_DATE":
		return now.Format("2006-01-02")
	case "CURTIME", "CURRENT_TIME":
		return now.Format("15:04:05")
	case "UTC_TIMESTAMP":
		return now
	case "UTC_DATE":
		return now.Format("2006-01-02")
	case "UTC_TIME":
		return now.Format("15:04:05")
	}
	return now
}

// evalFunc evaluates a SQL function call to a Go value. We only handle
// scalar functions whose arguments are themselves Value-evaluable; the
// general case (functions over column references in UPDATE/SET) requires
// MongoDB aggregation pipeline updates which we don't implement here.
func evalFunc(f *sqlparser.FuncExpr) (interface{}, error) {
	name := strings.ToUpper(f.Name.String())

	// Resolve argument values eagerly.
	args := make([]interface{}, 0, len(f.Exprs))
	for _, ae := range f.Exprs {
		v, err := Value(ae)
		if err != nil {
			// For column references inside scalar contexts we can't evaluate
			// statically; bubble up.
			return nil, fmt.Errorf("function %s: %w", name, err)
		}
		args = append(args, v)
	}

	switch name {
	// ───── time / date ─────
	case "NOW", "CURRENT_TIMESTAMP", "LOCALTIMESTAMP", "LOCALTIME", "SYSDATE",
		"UTC_TIMESTAMP":
		return time.Now().UTC(), nil
	case "CURDATE", "CURRENT_DATE", "UTC_DATE":
		return time.Now().UTC().Format("2006-01-02"), nil
	case "CURTIME", "CURRENT_TIME", "UTC_TIME":
		return time.Now().UTC().Format("15:04:05"), nil
	case "UNIX_TIMESTAMP":
		if len(args) == 0 {
			return time.Now().Unix(), nil
		}
		if t, ok := args[0].(time.Time); ok {
			return t.Unix(), nil
		}
		return nil, fmt.Errorf("UNIX_TIMESTAMP: argument must be a time")
	case "FROM_UNIXTIME":
		if len(args) == 0 {
			return nil, fmt.Errorf("FROM_UNIXTIME: missing argument")
		}
		ts, err := toInt64(args[0])
		if err != nil {
			return nil, fmt.Errorf("FROM_UNIXTIME: %w", err)
		}
		return time.Unix(ts, 0).UTC(), nil
	case "DATE":
		if len(args) == 0 {
			return nil, fmt.Errorf("DATE: missing argument")
		}
		if t, ok := args[0].(time.Time); ok {
			return t.Format("2006-01-02"), nil
		}
		return fmt.Sprint(args[0]), nil

	// ───── string ─────
	case "UPPER", "UCASE":
		if len(args) == 0 {
			return nil, fmt.Errorf("UPPER: missing argument")
		}
		return strings.ToUpper(toString(args[0])), nil
	case "LOWER", "LCASE":
		if len(args) == 0 {
			return nil, fmt.Errorf("LOWER: missing argument")
		}
		return strings.ToLower(toString(args[0])), nil
	case "LENGTH", "CHAR_LENGTH", "CHARACTER_LENGTH":
		if len(args) == 0 {
			return nil, fmt.Errorf("LENGTH: missing argument")
		}
		return int64(len(toString(args[0]))), nil
	case "TRIM":
		if len(args) == 0 {
			return nil, fmt.Errorf("TRIM: missing argument")
		}
		return strings.TrimSpace(toString(args[0])), nil
	case "LTRIM":
		if len(args) == 0 {
			return nil, fmt.Errorf("LTRIM: missing argument")
		}
		return strings.TrimLeft(toString(args[0]), " \t\n\r"), nil
	case "RTRIM":
		if len(args) == 0 {
			return nil, fmt.Errorf("RTRIM: missing argument")
		}
		return strings.TrimRight(toString(args[0]), " \t\n\r"), nil
	case "CONCAT":
		var b strings.Builder
		for _, a := range args {
			if a == nil {
				return nil, nil // MySQL returns NULL if any arg is NULL
			}
			b.WriteString(toString(a))
		}
		return b.String(), nil
	case "CONCAT_WS":
		if len(args) < 2 {
			return nil, fmt.Errorf("CONCAT_WS: at least 2 args required")
		}
		if args[0] == nil {
			return nil, nil
		}
		sep := toString(args[0])
		parts := make([]string, 0, len(args)-1)
		for _, a := range args[1:] {
			if a != nil {
				parts = append(parts, toString(a))
			}
		}
		return strings.Join(parts, sep), nil
	case "SUBSTRING", "SUBSTR", "MID":
		if len(args) < 2 {
			return nil, fmt.Errorf("SUBSTRING: at least 2 args required")
		}
		s := toString(args[0])
		start, err := toInt64(args[1])
		if err != nil {
			return nil, fmt.Errorf("SUBSTRING start: %w", err)
		}
		// MySQL is 1-indexed; negative counts from end.
		if start > 0 {
			start--
		} else if start < 0 {
			start = int64(len(s)) + start
			if start < 0 {
				start = 0
			}
		}
		if start >= int64(len(s)) {
			return "", nil
		}
		end := int64(len(s))
		if len(args) >= 3 {
			length, err := toInt64(args[2])
			if err != nil {
				return nil, fmt.Errorf("SUBSTRING length: %w", err)
			}
			end = start + length
			if end > int64(len(s)) {
				end = int64(len(s))
			}
		}
		return s[start:end], nil
	case "REPLACE":
		if len(args) < 3 {
			return nil, fmt.Errorf("REPLACE: 3 args required")
		}
		return strings.ReplaceAll(toString(args[0]), toString(args[1]), toString(args[2])), nil
	case "REVERSE":
		if len(args) == 0 {
			return nil, fmt.Errorf("REVERSE: missing argument")
		}
		runes := []rune(toString(args[0]))
		for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
			runes[i], runes[j] = runes[j], runes[i]
		}
		return string(runes), nil
	case "LEFT":
		if len(args) < 2 {
			return nil, fmt.Errorf("LEFT: 2 args required")
		}
		s := toString(args[0])
		n, err := toInt64(args[1])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			n = 0
		}
		if n > int64(len(s)) {
			n = int64(len(s))
		}
		return s[:n], nil
	case "RIGHT":
		if len(args) < 2 {
			return nil, fmt.Errorf("RIGHT: 2 args required")
		}
		s := toString(args[0])
		n, err := toInt64(args[1])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			n = 0
		}
		if n > int64(len(s)) {
			n = int64(len(s))
		}
		return s[int64(len(s))-n:], nil
	case "REPEAT":
		if len(args) < 2 {
			return nil, fmt.Errorf("REPEAT: 2 args required")
		}
		n, err := toInt64(args[1])
		if err != nil {
			return nil, err
		}
		if n < 0 {
			n = 0
		}
		return strings.Repeat(toString(args[0]), int(n)), nil
	case "LPAD":
		if len(args) < 3 {
			return nil, fmt.Errorf("LPAD: 3 args required")
		}
		s := toString(args[0])
		n, err := toInt64(args[1])
		if err != nil {
			return nil, err
		}
		pad := toString(args[2])
		return padString(s, int(n), pad, true), nil
	case "RPAD":
		if len(args) < 3 {
			return nil, fmt.Errorf("RPAD: 3 args required")
		}
		s := toString(args[0])
		n, err := toInt64(args[1])
		if err != nil {
			return nil, err
		}
		pad := toString(args[2])
		return padString(s, int(n), pad, false), nil

	// ───── numeric ─────
	case "ABS":
		if len(args) == 0 {
			return nil, fmt.Errorf("ABS: missing argument")
		}
		switch n := args[0].(type) {
		case int64:
			if n < 0 {
				return -n, nil
			}
			return n, nil
		case float64:
			if n < 0 {
				return -n, nil
			}
			return n, nil
		}
		return args[0], nil
	case "CEIL", "CEILING":
		f, err := toFloat64(args[0])
		if err != nil {
			return nil, err
		}
		return int64(math.Ceil(f)), nil
	case "FLOOR":
		f, err := toFloat64(args[0])
		if err != nil {
			return nil, err
		}
		return int64(math.Floor(f)), nil
	case "ROUND":
		f, err := toFloat64(args[0])
		if err != nil {
			return nil, err
		}
		decimals := int64(0)
		if len(args) >= 2 {
			decimals, _ = toInt64(args[1])
		}
		mult := 1.0
		for i := int64(0); i < decimals; i++ {
			mult *= 10
		}
		if f >= 0 {
			return float64(int64(f*mult+0.5)) / mult, nil
		}
		return float64(int64(f*mult-0.5)) / mult, nil
	case "MOD":
		if len(args) < 2 {
			return nil, fmt.Errorf("MOD: 2 args required")
		}
		bf, _ := toFloat64(args[1])
		if bf == 0 {
			return nil, nil
		}
		af, _ := toFloat64(args[0])
		// Integer operands keep integer result; otherwise MySQL MOD works on
		// reals (e.g. MOD(5.5, 2) = 1.5).
		if isIntegerValue(args[0]) && isIntegerValue(args[1]) {
			return int64(af) % int64(bf), nil
		}
		return math.Mod(af, bf), nil
	case "POW", "POWER":
		if len(args) < 2 {
			return nil, fmt.Errorf("POW: 2 args required")
		}
		a, _ := toFloat64(args[0])
		b, _ := toFloat64(args[1])
		return math.Pow(a, b), nil
	case "GREATEST":
		if len(args) == 0 {
			return nil, nil
		}
		max := args[0]
		for _, a := range args[1:] {
			if compareAny(a, max) > 0 {
				max = a
			}
		}
		return max, nil
	case "LEAST":
		if len(args) == 0 {
			return nil, nil
		}
		min := args[0]
		for _, a := range args[1:] {
			if compareAny(a, min) < 0 {
				min = a
			}
		}
		return min, nil

	// ───── conditional / null handling ─────
	case "IFNULL", "COALESCE":
		for _, a := range args {
			if a != nil {
				return a, nil
			}
		}
		return nil, nil
	case "NULLIF":
		if len(args) < 2 {
			return nil, fmt.Errorf("NULLIF: 2 args required")
		}
		if compareAny(args[0], args[1]) == 0 {
			return nil, nil
		}
		return args[0], nil
	case "IF":
		if len(args) < 3 {
			return nil, fmt.Errorf("IF: 3 args required")
		}
		if isTruthy(args[0]) {
			return args[1], nil
		}
		return args[2], nil
	case "ISNULL":
		if len(args) == 0 {
			return nil, nil
		}
		if args[0] == nil {
			return int64(1), nil
		}
		return int64(0), nil

	// ───── identity / misc ─────
	case "UUID":
		return generateUUID(), nil
	case "DATABASE", "SCHEMA":
		return "", nil
	case "USER", "CURRENT_USER", "SESSION_USER", "SYSTEM_USER":
		return "root@localhost", nil
	case "VERSION":
		return "8.0.0-mongodb-sql-driver", nil
	case "CONNECTION_ID":
		return int64(1), nil
	case "LAST_INSERT_ID", "ROW_COUNT", "FOUND_ROWS":
		return int64(0), nil

	// ───── casts ─────
	case "CAST", "CONVERT":
		// We can't easily evaluate CAST args (vitess uses dedicated AST
		// nodes), so just return the first arg untouched.
		if len(args) > 0 {
			return args[0], nil
		}
		return nil, nil
	}

	return nil, fmt.Errorf("unsupported function: %s", name)
}

// toString coerces v to its MySQL-style string representation.
func toString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case time.Time:
		return x.Format("2006-01-02 15:04:05")
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}

// toInt64 best-effort conversion to int64.
func toInt64(v interface{}) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case int:
		return int64(x), nil
	case float64:
		return int64(x), nil
	case string:
		return strconv.ParseInt(x, 10, 64)
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	}
	return 0, fmt.Errorf("cannot convert %T to int64", v)
}

// toFloat64 best-effort conversion to float64.
func toFloat64(v interface{}) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	case int:
		return float64(x), nil
	case string:
		return strconv.ParseFloat(x, 64)
	}
	return 0, fmt.Errorf("cannot convert %T to float64", v)
}

func compareAny(a, b interface{}) int {
	af, aerr := toFloat64(a)
	bf, berr := toFloat64(b)
	if aerr == nil && berr == nil {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		}
		return 0
	}
	as := toString(a)
	bs := toString(b)
	return strings.Compare(as, bs)
}

func isTruthy(v interface{}) bool {
	if v == nil {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		n, err := strconv.ParseFloat(x, 64)
		if err == nil {
			return n != 0
		}
		return x != ""
	}
	return true
}

func padString(s string, n int, pad string, left bool) string {
	if n <= len(s) {
		return s[:n]
	}
	if pad == "" {
		return s
	}
	need := n - len(s)
	var fill strings.Builder
	for fill.Len() < need {
		fill.WriteString(pad)
	}
	out := fill.String()[:need]
	if left {
		return out + s
	}
	return s + out
}

func generateUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	// RFC 4122 v4
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	)
}

// ColName converts a column reference to a string usable as a Mongo field path.
// Honors qualifier so that table.col becomes table.col (matching $lookup output).
func ColName(c *sqlparser.ColName) string {
	name := c.Name.String()
	if !c.Qualifier.IsEmpty() {
		return c.Qualifier.Name.String() + "." + name
	}
	return name
}

// LikeToRegex converts a SQL LIKE pattern into a Go regular expression.
func LikeToRegex(pattern string) string {
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(pattern) {
		c := pattern[i]
		switch c {
		case '\\':
			if i+1 < len(pattern) {
				b.WriteString(regexp.QuoteMeta(string(pattern[i+1])))
				i += 2
				continue
			}
			b.WriteString(regexp.QuoteMeta(string(c)))
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
		i++
	}
	b.WriteString("$")
	return b.String()
}

// TranslateWhere converts a WHERE/HAVING expression to a Mongo filter document.
func TranslateWhere(e sqlparser.Expr) (bson.M, error) {
	if e == nil {
		return bson.M{}, nil
	}
	switch v := e.(type) {
	case *sqlparser.AndExpr:
		l, err := TranslateWhere(v.Left)
		if err != nil {
			return nil, err
		}
		r, err := TranslateWhere(v.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{"$and": []bson.M{l, r}}, nil

	case *sqlparser.OrExpr:
		l, err := TranslateWhere(v.Left)
		if err != nil {
			return nil, err
		}
		r, err := TranslateWhere(v.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{"$or": []bson.M{l, r}}, nil

	case *sqlparser.NotExpr:
		return negateWhere(v.Expr)

	case *sqlparser.ComparisonExpr:
		return translateComparison(v)

	case *sqlparser.BetweenExpr:
		col, ok := v.Left.(*sqlparser.ColName)
		if !ok {
			return nil, fmt.Errorf("BETWEEN left side must be a column")
		}
		from, err := Value(v.From)
		if err != nil {
			return nil, err
		}
		to, err := Value(v.To)
		if err != nil {
			return nil, err
		}
		field := ColName(col)
		if v.IsBetween {
			return bson.M{field: bson.M{"$gte": from, "$lte": to}}, nil
		}
		return bson.M{"$or": []bson.M{
			{field: bson.M{"$lt": from}},
			{field: bson.M{"$gt": to}},
		}}, nil

	case *sqlparser.IsExpr:
		col, ok := v.Left.(*sqlparser.ColName)
		if !ok {
			return nil, fmt.Errorf("IS expression left side must be a column")
		}
		field := ColName(col)
		switch v.Right {
		case sqlparser.IsNullOp:
			return bson.M{field: bson.M{"$eq": nil}}, nil
		case sqlparser.IsNotNullOp:
			return bson.M{field: bson.M{"$ne": nil}}, nil
		case sqlparser.IsTrueOp:
			return bson.M{field: bson.M{"$eq": true}}, nil
		case sqlparser.IsNotTrueOp:
			return bson.M{field: bson.M{"$ne": true}}, nil
		case sqlparser.IsFalseOp:
			return bson.M{field: bson.M{"$eq": false}}, nil
		case sqlparser.IsNotFalseOp:
			return bson.M{field: bson.M{"$ne": false}}, nil
		}
	}
	return nil, fmt.Errorf("unsupported WHERE expression: %T", e)
}

// negateWhere translates NOT(e) using SQL three-valued logic by pushing the
// negation down to leaf predicates, so that rows where e is NULL/unknown are
// excluded (matching MySQL) rather than included as a bare $nor would.
func negateWhere(e sqlparser.Expr) (bson.M, error) {
	switch v := e.(type) {
	case *sqlparser.NotExpr:
		// NOT NOT x = x
		return TranslateWhere(v.Expr)
	case *sqlparser.AndExpr:
		l, err := negateWhere(v.Left)
		if err != nil {
			return nil, err
		}
		r, err := negateWhere(v.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{"$or": []bson.M{l, r}}, nil
	case *sqlparser.OrExpr:
		l, err := negateWhere(v.Left)
		if err != nil {
			return nil, err
		}
		r, err := negateWhere(v.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{"$and": []bson.M{l, r}}, nil
	case *sqlparser.ComparisonExpr:
		if neg, ok := negateComparisonOp(v.Operator); ok {
			nc := *v
			nc.Operator = neg
			return translateComparison(&nc)
		}
	case *sqlparser.IsExpr:
		nc := *v
		nc.Right = negateIsOp(v.Right)
		return TranslateWhere(&nc)
	case *sqlparser.BetweenExpr:
		nc := *v
		nc.IsBetween = !v.IsBetween
		return TranslateWhere(&nc)
	}
	// Fallback for shapes without a clean negation: structural $nor (may
	// include NULL rows, but preserves the previous behaviour).
	inner, err := TranslateWhere(e)
	if err != nil {
		return nil, err
	}
	return bson.M{"$nor": []bson.M{inner}}, nil
}

func negateComparisonOp(op sqlparser.ComparisonExprOperator) (sqlparser.ComparisonExprOperator, bool) {
	switch op {
	case sqlparser.EqualOp:
		return sqlparser.NotEqualOp, true
	case sqlparser.NotEqualOp:
		return sqlparser.EqualOp, true
	case sqlparser.LessThanOp:
		return sqlparser.GreaterEqualOp, true
	case sqlparser.LessEqualOp:
		return sqlparser.GreaterThanOp, true
	case sqlparser.GreaterThanOp:
		return sqlparser.LessEqualOp, true
	case sqlparser.GreaterEqualOp:
		return sqlparser.LessThanOp, true
	case sqlparser.InOp:
		return sqlparser.NotInOp, true
	case sqlparser.NotInOp:
		return sqlparser.InOp, true
	case sqlparser.LikeOp:
		return sqlparser.NotLikeOp, true
	case sqlparser.NotLikeOp:
		return sqlparser.LikeOp, true
	case sqlparser.RegexpOp:
		return sqlparser.NotRegexpOp, true
	case sqlparser.NotRegexpOp:
		return sqlparser.RegexpOp, true
	}
	return op, false
}

func negateIsOp(op sqlparser.IsExprOperator) sqlparser.IsExprOperator {
	switch op {
	case sqlparser.IsNullOp:
		return sqlparser.IsNotNullOp
	case sqlparser.IsNotNullOp:
		return sqlparser.IsNullOp
	case sqlparser.IsTrueOp:
		return sqlparser.IsNotTrueOp
	case sqlparser.IsNotTrueOp:
		return sqlparser.IsTrueOp
	case sqlparser.IsFalseOp:
		return sqlparser.IsNotFalseOp
	case sqlparser.IsNotFalseOp:
		return sqlparser.IsFalseOp
	}
	return op
}

// exprOpToMongo maps a SQL comparison operator to the corresponding
// MongoDB aggregation operator. Returns an empty string for operators that
// can't be expressed via $expr (e.g. LIKE / IN / REGEXP — those keep their
// historical translation).
func exprOpToMongo(op sqlparser.ComparisonExprOperator) string {
	switch op {
	case sqlparser.EqualOp, sqlparser.NullSafeEqualOp:
		return "$eq"
	case sqlparser.NotEqualOp:
		return "$ne"
	case sqlparser.LessThanOp:
		return "$lt"
	case sqlparser.LessEqualOp:
		return "$lte"
	case sqlparser.GreaterThanOp:
		return "$gt"
	case sqlparser.GreaterEqualOp:
		return "$gte"
	}
	return ""
}

func translateComparison(c *sqlparser.ComparisonExpr) (bson.M, error) {
	// When either side references a column (other than the simple
	// "column OP literal" form), fall back to a $expr-based filter so that
	// expressions like `WHERE a = b` or `WHERE a > b + 1` work.
	if mongoOp := exprOpToMongo(c.Operator); mongoOp != "" {
		_, leftIsCol := c.Left.(*sqlparser.ColName)
		_, rightIsConst := c.Right.(*sqlparser.Literal)
		if rb, isBool := c.Right.(sqlparser.BoolVal); isBool {
			_ = rb
			rightIsConst = true
		}
		if _, isNull := c.Right.(*sqlparser.NullVal); isNull {
			rightIsConst = true
		}
		// Use the historical fast-path (column-indexed filter) only when
		// left is a bare column and right is a pure constant.
		if !(leftIsCol && rightIsConst) {
			left, err := ToAggExpr(c.Left)
			if err != nil {
				return nil, fmt.Errorf("comparison left: %w", err)
			}
			right, err := ToAggExpr(c.Right)
			if err != nil {
				return nil, fmt.Errorf("comparison right: %w", err)
			}
			return bson.M{"$expr": bson.M{mongoOp: []interface{}{left, right}}}, nil
		}
	}

	col, ok := c.Left.(*sqlparser.ColName)
	if !ok {
		return nil, fmt.Errorf("comparison left side must be a column, got %T", c.Left)
	}
	field := ColName(col)

	switch c.Operator {
	case sqlparser.EqualOp, sqlparser.NullSafeEqualOp:
		v, err := Value(c.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$eq": v}}, nil
	case sqlparser.NotEqualOp:
		v, err := Value(c.Right)
		if err != nil {
			return nil, err
		}
		if v == nil {
			return bson.M{field: bson.M{"$ne": nil}}, nil
		}
		// MySQL excludes NULLs from "col != v" (three-valued logic); a bare
		// $ne would also match missing/NULL fields, so require non-NULL too.
		return bson.M{"$and": []bson.M{
			{field: bson.M{"$ne": v}},
			{field: bson.M{"$ne": nil}},
		}}, nil
	case sqlparser.LessThanOp:
		v, err := Value(c.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$lt": v}}, nil
	case sqlparser.LessEqualOp:
		v, err := Value(c.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$lte": v}}, nil
	case sqlparser.GreaterThanOp:
		v, err := Value(c.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$gt": v}}, nil
	case sqlparser.GreaterEqualOp:
		v, err := Value(c.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$gte": v}}, nil
	case sqlparser.InOp:
		v, err := Value(c.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$in": v}}, nil
	case sqlparser.NotInOp:
		v, err := Value(c.Right)
		if err != nil {
			return nil, err
		}
		return bson.M{field: bson.M{"$nin": v}}, nil
	case sqlparser.LikeOp:
		lit, ok := c.Right.(*sqlparser.Literal)
		if !ok || lit.Type != sqlparser.StrVal {
			return nil, fmt.Errorf("LIKE right side must be a string literal")
		}
		// MySQL LIKE is case-insensitive under the default *_ci collation.
		return bson.M{field: bson.Regex{Pattern: LikeToRegex(lit.Val), Options: "i"}}, nil
	case sqlparser.NotLikeOp:
		lit, ok := c.Right.(*sqlparser.Literal)
		if !ok || lit.Type != sqlparser.StrVal {
			return nil, fmt.Errorf("NOT LIKE right side must be a string literal")
		}
		return bson.M{field: bson.M{"$not": bson.Regex{Pattern: LikeToRegex(lit.Val), Options: "i"}}}, nil
	case sqlparser.RegexpOp:
		lit, ok := c.Right.(*sqlparser.Literal)
		if !ok || lit.Type != sqlparser.StrVal {
			return nil, fmt.Errorf("REGEXP right side must be a string literal")
		}
		return bson.M{field: bson.M{"$regex": lit.Val}}, nil
	case sqlparser.NotRegexpOp:
		lit, ok := c.Right.(*sqlparser.Literal)
		if !ok || lit.Type != sqlparser.StrVal {
			return nil, fmt.Errorf("NOT REGEXP right side must be a string literal")
		}
		return bson.M{field: bson.M{"$not": bson.M{"$regex": lit.Val}}}, nil
	}
	return nil, fmt.Errorf("unsupported comparison operator: %s", c.Operator.ToString())
}
