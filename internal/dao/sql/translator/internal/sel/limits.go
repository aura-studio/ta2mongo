package sel

import (
	"fmt"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/aura-studio/tango/internal/dao/sql/translator/internal/expr"
)

func buildSort(o sqlparser.OrderBy) (bson.D, error) {
	if len(o) == 0 {
		return nil, nil
	}
	out := make(bson.D, 0, len(o))
	for _, ord := range o {
		cn, ok := ord.Expr.(*sqlparser.ColName)
		if !ok {
			return nil, fmt.Errorf("ORDER BY only supports column references")
		}
		dir := 1
		if ord.Direction == sqlparser.DescOrder {
			dir = -1
		}
		out = append(out, bson.E{Key: expr.ColName(cn), Value: dir})
	}
	return out, nil
}

func buildLimit(l *sqlparser.Limit) (limit, skip int64, hasLimit bool, err error) {
	if l == nil {
		return 0, 0, false, nil
	}
	if l.Rowcount != nil {
		v, err := expr.Value(l.Rowcount)
		if err != nil {
			return 0, 0, false, err
		}
		limit = toInt64(v)
		hasLimit = true
	}
	if l.Offset != nil {
		v, err := expr.Value(l.Offset)
		if err != nil {
			return 0, 0, false, err
		}
		skip = toInt64(v)
	}
	return
}

func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case string:
		x, _ := strconv.ParseInt(n, 10, 64)
		return x
	}
	return 0
}
