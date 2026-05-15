package store

import (
	"reflect"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
)

func TestTrackTsCondSetNoOpPipelineStructure(t *testing.T) {
	ts := int64(123)

	stage := trackTsCondSetNoOp(ts, bsonM(map[string]any{
		"a": "v1",
		// "_ts" is intentionally ignored by the builder
		"_ts": int64(999),
	}))

	// _ts stage must advance forward via $max
	expectedMax := bson.M{
		"$max": bson.A{"$_ts", ts},
	}
	if got := stage["_ts"]; !reflect.DeepEqual(got, expectedMax) {
		t.Fatalf("unexpected _ts stage: got=%v expected=%v", got, expectedMax)
	}

	// Field "a" should use $cond with strict no-create semantics:
	// - if: ts >= ifNull($_ts, 0)
	// - then: "v1"
	// - else: ifNull("$a", "$$REMOVE") => $$REMOVE will be used for missing fields
	aStage, ok := stage["a"].(bson.M)
	if !ok {
		t.Fatalf("expected stage['a'] to be bson.M, got=%T (%v)", stage["a"], stage["a"])
	}

	condObj, ok := aStage["$cond"].(bson.M)
	if !ok {
		t.Fatalf("expected stage['a'].$cond to be bson.M, got=%T (%v)", aStage["$cond"], aStage["$cond"])
	}

	ifCond, ok := condObj["if"].(bson.M)
	if !ok {
		t.Fatalf("expected $cond.if to be bson.M, got=%T (%v)", condObj["if"], condObj["if"])
	}
	if _, ok := ifCond["$gte"]; !ok {
		t.Fatalf("expected $cond.if to contain $gte, got=%v", ifCond)
	}

	if condObj["then"] != "v1" {
		t.Fatalf("expected then='v1', got=%v", condObj["then"])
	}

	elseExpr, ok := condObj["else"].(bson.M)
	if !ok {
		t.Fatalf("expected $cond.else to be bson.M, got=%T (%v)", condObj["else"], condObj["else"])
	}

	if _, ok := elseExpr["$ifNull"]; !ok {
		t.Fatalf("expected else to contain $ifNull, got=%v", elseExpr)
	}

	elseArgs, ok := elseExpr["$ifNull"].(bson.A)
	if !ok {
		t.Fatalf("expected elseExpr['$ifNull'] to be bson.A, got=%T (%v)", elseExpr["$ifNull"], elseExpr["$ifNull"])
	}
	if len(elseArgs) != 2 {
		t.Fatalf("expected $ifNull args length=2, got=%d (%v)", len(elseArgs), elseArgs)
	}
	if elseArgs[1] != "$$REMOVE" {
		t.Fatalf("expected strict no-create via $$REMOVE, got=%v", elseArgs[1])
	}

	// Also ensure pipeline does not try to write "_ts" as a conditional field.
	if _, exists := stage["_ts"]; !exists {
		t.Fatalf("expected stage['_ts'] to exist")
	}
}

func TestTrackTsCondReplacePipelineStructure(t *testing.T) {
	ts := int64(7)
	doc := bsonM(map[string]any{
		"_ts": ts,
		"x":   1,
	})

	pipeline := trackTsCondReplacePipeline(ts, doc)
	if len(pipeline) != 1 {
		t.Fatalf("expected pipeline length=1, got=%d", len(pipeline))
	}

	replaceWith, ok := pipeline[0].(bson.M)
	if !ok {
		t.Fatalf("expected pipeline[0] to be bson.M, got=%T (%v)", pipeline[0], pipeline[0])
	}

	rwExpr, ok := replaceWith["$replaceWith"].(bson.M)
	if !ok {
		t.Fatalf("expected $replaceWith to be bson.M, got=%T (%v)", replaceWith["$replaceWith"], replaceWith["$replaceWith"])
	}

	condArr, ok := rwExpr["$cond"].(bson.A)
	if !ok {
		t.Fatalf("expected $replaceWith.$cond to be bson.A, got=%T (%v)", rwExpr["$cond"], rwExpr["$cond"])
	}
	if len(condArr) != 3 {
		t.Fatalf("expected $cond array length=3, got=%d (%v)", len(condArr), condArr)
	}

	// condArr[1] must be {"$literal": doc}
	litObj, ok := condArr[1].(bson.M)
	if !ok {
		t.Fatalf("expected condArr[1] to be bson.M, got=%T (%v)", condArr[1], condArr[1])
	}
	if _, ok := litObj["$literal"]; !ok {
		t.Fatalf("expected $literal wrapper for full doc replacement, got=%v", condArr[1])
	}
	if !reflect.DeepEqual(litObj["$literal"], doc) {
		t.Fatalf("expected $literal doc to match, got=%v expected=%v", litObj["$literal"], doc)
	}

	// condArr[2] must keep existing doc unchanged: $$ROOT
	if condArr[2] != "$$ROOT" {
		t.Fatalf("expected $$ROOT for no-op on older _ts, got=%v", condArr[2])
	}
}

func bsonM(m map[string]any) bson.M { return bson.M(m) }
