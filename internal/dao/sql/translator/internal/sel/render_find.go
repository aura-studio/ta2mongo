package sel

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/dao/sql/translator/plan"
	"github.com/aura-studio/tango/internal/dao/sql/translator/stmt"
)

func buildFind(p *plan.SelectPlan) (stmt.Statement, error) {
	projection, err := buildFindProjection(p)
	if err != nil {
		return nil, err
	}

	out := &stmt.FindStmt{
		Collection: p.MainSource.Collection,
		Filter:     p.Filter,
		Projection: projection,
		Sort:       p.Sort,
		Limit:      p.Limit,
		Skip:       p.Offset,
		// An explicit LIMIT 0 selects zero rows (MySQL semantics); MongoDB
		// treats Find limit 0 as "no limit", so mark the result statically empty.
		Empty: p.HasLimit && p.Limit == 0,
	}

	if p.Distinct && len(p.Items) == 1 && !p.HasStar {
		if p.Items[0].Field != nil {
			out.Distinct = p.Items[0].Field.Path()
		}
	}

	return out, nil
}

func buildFindProjection(p *plan.SelectPlan) (bson.M, error) {
	if p.HasStar {
		// SELECT * — return user-defined fields only, hiding MongoDB's
		// auto-generated _id. SQL clients expect only the columns they
		// declared in CREATE TABLE / INSERT to appear.
		return bson.M{"_id": 0}, nil
	}
	projection := bson.M{}
	askedForID := false
	for _, item := range p.Items {
		if item.Kind != plan.SelectItemField || item.Field == nil {
			return nil, fmt.Errorf("unsupported select expression: %T", item.RawExpr)
		}
		name := item.Field.Path()
		projection[name] = 1
		if name == "_id" {
			askedForID = true
		}
	}
	if !askedForID {
		projection["_id"] = 0
	}
	return projection, nil
}
