package store

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// TestUserWriteModel_UnsetStructure pins the shape of the user_unset write
// model. It guards the fragile pipeline[0].(bson.M)["$set"].(bson.M) assembly
// in UserWriteModel — a regression net for any future rewrite of that branch
// (the assembly runs at construction time, so a broken rewrite would panic
// here).
func TestUserWriteModel_UnsetStructure(t *testing.T) {
	doc := bson.M{
		"old_field": true,
		"_ts":       int64(100),
	}
	model := UserWriteModel("user_unset", 7, doc)

	upd, ok := model.(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("expected *mongo.UpdateOneModel, got %T", model)
	}
	if upd.Upsert == nil || !*upd.Upsert {
		t.Error("user_unset model must be an upsert")
	}
	if f, ok := upd.Filter.(bson.M); !ok || f["#user_id"] != int64(7) {
		t.Errorf("filter = %v, want {#user_id: 7}", upd.Filter)
	}

	pipeline, ok := upd.Update.(bson.A)
	if !ok || len(pipeline) == 0 {
		t.Fatalf("update = %T (%v), want non-empty bson.A pipeline", upd.Update, upd.Update)
	}
	stage0, ok := pipeline[0].(bson.M)
	if !ok {
		t.Fatalf("pipeline[0] = %T, want bson.M", pipeline[0])
	}
	set, ok := stage0["$set"].(bson.M)
	if !ok {
		t.Fatalf("pipeline[0].$set = %T, want bson.M", stage0["$set"])
	}
	// The first stage must conditionally advance _ts (timestamp ordering).
	if _, ok := set["_ts"]; !ok {
		t.Errorf("first $set stage missing _ts handling: %v", set)
	}
}
