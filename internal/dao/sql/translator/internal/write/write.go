// Package write translates INSERT, UPDATE, and DELETE statements.
package write

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/aura-studio/tango/internal/dao/sql/translator/internal/expr"
	"github.com/aura-studio/tango/internal/dao/sql/translator/internal/sel"
	"github.com/aura-studio/tango/internal/dao/sql/translator/stmt"
)

// Insert translates INSERT INTO ... VALUES (...) and INSERT INTO ... SELECT ...
func Insert(s *sqlparser.Insert) (stmt.Statement, error) {
	if s.Table == nil {
		return nil, fmt.Errorf("INSERT requires a target table")
	}
	tn, ok := s.Table.Expr.(sqlparser.TableName)
	if !ok {
		return nil, fmt.Errorf("INSERT target must be a table name")
	}
	coll := tn.Name.String()
	db := tn.Qualifier.String()

	if len(s.Columns) == 0 {
		return nil, fmt.Errorf("INSERT requires explicit column list")
	}

	// INSERT ... SELECT
	if selStmt, isSel := s.Rows.(*sqlparser.Select); isSel {
		return insertSelect(coll, db, s.Columns, selStmt)
	}

	values, ok := s.Rows.(sqlparser.Values)
	if !ok {
		return nil, fmt.Errorf("only INSERT ... VALUES or INSERT ... SELECT is supported, got %T", s.Rows)
	}

	cols := make([]string, len(s.Columns))
	for i, c := range s.Columns {
		cols[i] = c.String()
	}

	docs := make([]bson.M, 0, len(values))
	for _, row := range values {
		if len(row) != len(cols) {
			return nil, fmt.Errorf("column/value count mismatch")
		}
		doc := bson.M{}
		for i, e := range row {
			// INSERT values must be statically computable. Allow constant
			// arithmetic (`1+2`, `-1`, `'a'||...`) via ValueOrAggExpr but
			// reject any column reference — there's nothing to reference at
			// insert time.
			val, needsAgg, err := expr.ValueOrAggExpr(e)
			if err != nil {
				return nil, err
			}
			if needsAgg {
				return nil, fmt.Errorf("INSERT VALUES cannot reference columns")
			}
			doc[cols[i]] = val
		}
		docs = append(docs, doc)
	}
	return &stmt.InsertStmt{Collection: coll, Docs: docs}, nil
}

// Update translates UPDATE ... SET ... [WHERE ...].
func Update(s *sqlparser.Update) (stmt.Statement, error) {
	if len(s.TableExprs) != 1 {
		return nil, fmt.Errorf("only single-table UPDATE is supported")
	}
	at, ok := s.TableExprs[0].(*sqlparser.AliasedTableExpr)
	if !ok {
		return nil, fmt.Errorf("UPDATE: unsupported table expression: %T", s.TableExprs[0])
	}
	src, err := sel.SourceRefFromAliasedTable(at)
	if err != nil {
		return nil, err
	}

	// First pass: detect whether any RHS references a column. If yes, we
	// must emit a pipeline-style update (MongoDB 4.2+); otherwise we use
	// the classic {$set:{...}} form which preserves type fidelity for
	// scalars (and works on older deployments).
	needsAgg := false
	for _, ue := range s.Exprs {
		if expr.HasColumnRef(ue.Expr) {
			needsAgg = true
			break
		}
	}

	var update bson.M
	if needsAgg {
		// Build [{$set: {col: <aggExpr>}}].
		setDoc := bson.M{}
		for _, ue := range s.Exprs {
			ae, _, err := expr.ValueOrAggExpr(ue.Expr)
			if err != nil {
				return nil, fmt.Errorf("UPDATE SET %s: %w", ue.Name.Name.String(), err)
			}
			setDoc[ue.Name.Name.String()] = ae
		}
		// We still wrap as {$set: ...} so the driver layer can detect and
		// merge ON UPDATE columns in ApplyOnUpdate.
		update = bson.M{"$set": setDoc, "$pipeline": true}
	} else {
		setDoc := bson.M{}
		for _, ue := range s.Exprs {
			v, _, err := expr.ValueOrAggExpr(ue.Expr)
			if err != nil {
				return nil, err
			}
			setDoc[ue.Name.Name.String()] = v
		}
		update = bson.M{"$set": setDoc}
	}

	filter := bson.M{}
	if s.Where != nil {
		f, err := expr.TranslateWhere(s.Where.Expr)
		if err != nil {
			return nil, err
		}
		filter = f
	}
	return &stmt.UpdateStmt{Collection: src.Collection, Filter: filter, Update: update}, nil
}

// Delete translates DELETE FROM ... [WHERE ...].
func Delete(s *sqlparser.Delete) (stmt.Statement, error) {
	if len(s.TableExprs) != 1 {
		return nil, fmt.Errorf("only single-table DELETE is supported")
	}
	at, ok := s.TableExprs[0].(*sqlparser.AliasedTableExpr)
	if !ok {
		return nil, fmt.Errorf("DELETE: unsupported table expression: %T", s.TableExprs[0])
	}
	src, err := sel.SourceRefFromAliasedTable(at)
	if err != nil {
		return nil, err
	}

	filter := bson.M{}
	if s.Where != nil {
		f, err := expr.TranslateWhere(s.Where.Expr)
		if err != nil {
			return nil, err
		}
		filter = f
	}
	return &stmt.DeleteStmt{Collection: src.Collection, Filter: filter}, nil
}

// insertSelect translates INSERT INTO target (cols) SELECT ... FROM source.
func insertSelect(targetColl, targetDB string, columns sqlparser.Columns, selStmt *sqlparser.Select) (stmt.Statement, error) {
	// Compile the SELECT to get an AggregateStmt pipeline.
	selResult, err := sel.Translate(selStmt)
	if err != nil {
		return nil, fmt.Errorf("INSERT ... SELECT: %w", err)
	}

	cols := make([]string, len(columns))
	for i, c := range columns {
		cols[i] = c.String()
	}

	switch s := selResult.(type) {
	case *stmt.AggregateStmt:
		return &stmt.InsertSelectStmt{
			SourceCollection: s.Collection,
			TargetCollection: targetColl,
			TargetDatabase:   targetDB,
			Pipeline:         s.Pipeline,
			Columns:          cols,
		}, nil
	case *stmt.FindStmt:
		// Convert the Find to a minimal aggregation pipeline.
		pipeline := make([]bson.M, 0, 4)
		if len(s.Filter) > 0 {
			pipeline = append(pipeline, bson.M{"$match": s.Filter})
		}
		if s.Projection != nil {
			pipeline = append(pipeline, bson.M{"$project": s.Projection})
		}
		if s.Sort != nil {
			sortM := bson.M{}
			for _, e := range s.Sort {
				sortM[e.Key] = e.Value
			}
			pipeline = append(pipeline, bson.M{"$sort": sortM})
		}
		if s.Skip > 0 {
			pipeline = append(pipeline, bson.M{"$skip": s.Skip})
		}
		if s.Limit > 0 {
			pipeline = append(pipeline, bson.M{"$limit": s.Limit})
		}
		return &stmt.InsertSelectStmt{
			SourceCollection: s.Collection,
			TargetCollection: targetColl,
			TargetDatabase:   targetDB,
			Pipeline:         pipeline,
			Columns:          cols,
		}, nil
	}
	return nil, fmt.Errorf("INSERT ... SELECT produced unexpected statement type: %T", selResult)
}
