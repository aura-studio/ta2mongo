package store

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// TestUserWriteModel_UnsetStructure pins the DocumentDB-compatible shape of the
// user_unset write model: a plain { $set: meta, $unset: {field: ""} } update
// guarded by an _ts filter (no aggregation pipeline).
func TestUserWriteModel_UnsetStructure(t *testing.T) {
	ts := int64(100)
	doc := bson.M{
		"old_field": true,
		"_ts":       ts,
	}
	model := UserWriteModel("user_unset", 7, doc)

	upd, ok := model.(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("expected *mongo.UpdateOneModel, got %T", model)
	}
	if upd.Upsert == nil || !*upd.Upsert {
		t.Error("user_unset model must be an upsert")
	}

	update := assertPlainUpdate(t, upd.Update)

	// $set must carry the meta fields, including _ts.
	set, ok := update["$set"].(bson.M)
	if !ok {
		t.Fatalf("update.$set = %T, want bson.M", update["$set"])
	}
	if set["_ts"] != ts {
		t.Errorf("$set._ts = %v, want %v", set["_ts"], ts)
	}
	if set["#user_id"] != int64(7) {
		t.Errorf("$set.#user_id = %v, want 7", set["#user_id"])
	}

	// $unset must target the data field, and must not overlap with $set.
	unset, ok := update["$unset"].(bson.M)
	if !ok {
		t.Fatalf("update.$unset = %T, want bson.M", update["$unset"])
	}
	if _, ok := unset["old_field"]; !ok {
		t.Errorf("$unset missing old_field: %v", unset)
	}
	if _, ok := set["old_field"]; ok {
		t.Errorf("old_field must not appear in both $set and $unset")
	}

	assertTsGuardFilter(t, upd.Filter, "#user_id", int64(7), ts)
}
