package store

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// assertTsGuardFilter checks that a filter has the DocumentDB-compatible shape
//
//	{ <key>: <val>, $or: [ {_ts:{$exists:false}}, {_ts:{$lte: ts}} ] }
//
// produced by the _ts filter-guard write models (see tsGuardOr).
func assertTsGuardFilter(t *testing.T, filter any, key string, val, ts any) {
	t.Helper()
	f, ok := filter.(bson.M)
	if !ok {
		t.Fatalf("filter = %T, want bson.M", filter)
	}
	if f[key] != val {
		t.Errorf("filter[%q] = %v, want %v", key, f[key], val)
	}
	or, ok := f["$or"].(bson.A)
	if !ok || len(or) != 2 {
		t.Fatalf("filter $or = %T (%v), want bson.A of length 2", f["$or"], f["$or"])
	}
	// First clause: {_ts: {$exists: false}}.
	c0, ok := or[0].(bson.M)
	if !ok {
		t.Fatalf("$or[0] = %T, want bson.M", or[0])
	}
	if ex, ok := c0["_ts"].(bson.M); !ok || ex["$exists"] != false {
		t.Errorf("$or[0] = %v, want {_ts:{$exists:false}}", or[0])
	}
	// Second clause: {_ts: {$lte: ts}}.
	c1, ok := or[1].(bson.M)
	if !ok {
		t.Fatalf("$or[1] = %T, want bson.M", or[1])
	}
	lte, ok := c1["_ts"].(bson.M)
	if !ok {
		t.Fatalf("$or[1]._ts = %T, want bson.M", c1["_ts"])
	}
	if lte["$lte"] != ts {
		t.Errorf("$or[1]._ts.$lte = %v, want %v", lte["$lte"], ts)
	}
}

// assertPlainUpdate fails if update is an aggregation pipeline (bson.A) instead
// of a plain document — DocumentDB rejects pipeline-form updates with
// "Wrong type for parameter u", which is exactly the bug these models fix.
func assertPlainUpdate(t *testing.T, update any) bson.M {
	t.Helper()
	if _, isPipeline := update.(bson.A); isPipeline {
		t.Fatalf("update is a bson.A pipeline; DocumentDB requires a plain document")
	}
	m, ok := update.(bson.M)
	if !ok {
		t.Fatalf("update = %T, want bson.M", update)
	}
	return m
}

// TestUserWriteModel_UserSet_FilterGuard pins the DocumentDB-compatible shape of
// user_set: a plain {$set: ...} update guarded by an _ts filter.
func TestUserWriteModel_UserSet_FilterGuard(t *testing.T) {
	ts := int64(123)
	model := UserWriteModel("user_set", 7, bson.M{
		"#time": "2024-01-01",
		"_ts":   ts,
		"name":  "Alice",
	})

	upd, ok := model.(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("expected *mongo.UpdateOneModel, got %T", model)
	}
	if upd.Upsert == nil || !*upd.Upsert {
		t.Error("user_set model must be an upsert")
	}
	set := assertPlainUpdate(t, upd.Update)
	setDoc, ok := set["$set"].(bson.M)
	if !ok {
		t.Fatalf("update missing $set document: %v", set)
	}
	if setDoc["name"] != "Alice" || setDoc["#user_id"] != int64(7) || setDoc["_ts"] != ts {
		t.Errorf("$set = %v, want name/#user_id/_ts present", setDoc)
	}
	assertTsGuardFilter(t, upd.Filter, "#user_id", int64(7), ts)
}

// TestEventWriteModel_TrackUpdate_FilterGuard pins track_update: a plain
// {$set: doc} update guarded by an _ts filter.
func TestEventWriteModel_TrackUpdate_FilterGuard(t *testing.T) {
	ts := int64(123)
	doc := bson.M{"#uuid": "u1", "_ts": ts, "a": "v1"}
	model := EventWriteModel("track_update", "u1", doc)

	upd, ok := model.(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("expected *mongo.UpdateOneModel, got %T", model)
	}
	if upd.Upsert == nil || !*upd.Upsert {
		t.Error("track_update model must be an upsert")
	}
	set := assertPlainUpdate(t, upd.Update)
	if _, ok := set["$set"].(bson.M); !ok {
		t.Fatalf("update missing $set document: %v", set)
	}
	assertTsGuardFilter(t, upd.Filter, "#uuid", "u1", ts)
}

// TestEventWriteModel_TrackOverwrite_FilterGuard pins track_overwrite: a
// ReplaceOne guarded by an _ts filter.
func TestEventWriteModel_TrackOverwrite_FilterGuard(t *testing.T) {
	ts := int64(7)
	doc := bson.M{"#uuid": "u2", "_ts": ts, "x": 1}
	model := EventWriteModel("track_overwrite", "u2", doc)

	rep, ok := model.(*mongo.ReplaceOneModel)
	if !ok {
		t.Fatalf("expected *mongo.ReplaceOneModel, got %T", model)
	}
	if rep.Upsert == nil || !*rep.Upsert {
		t.Error("track_overwrite model must be an upsert")
	}
	if _, ok := rep.Replacement.(bson.M); !ok {
		t.Fatalf("replacement = %T, want bson.M", rep.Replacement)
	}
	assertTsGuardFilter(t, rep.Filter, "#uuid", "u2", ts)
}
