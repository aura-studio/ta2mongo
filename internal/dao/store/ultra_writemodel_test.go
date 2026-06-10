// Ultra test gap closure for doc/ultra_test.md:
//
//	WM-15 (P1): splitFields/metaKeys — exact meta vs data partition (unit).
//	WM-7  (P1): unknown user_* type falls back to user_set semantics (unit + integration).
//	WM-11 (P1): EventWriteModel injects #uuid from the uuid argument and _ts=now
//	            when the record lacks them (unit + integration).
//	IDX-2 (P1): EnsureIndexes creates exactly #user_id(unique), #account_id,
//	            #distinct_id and _ts on the user collection (integration).
//
// Integration tests reuse testSetup (testhelper_test.go): they connect via
// TANGO_TEST_MONGO_URI to a throwaway random database and skip when no Mongo
// is reachable.
package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// ---------------------------------------------------------------------------
// WM-15: splitFields / metaKeys
// ---------------------------------------------------------------------------

// TestUltraWM15_MetaKeysExactSet pins the metaKeys set to exactly the seven
// TA identifier/timestamp fields managed separately from business data.
func TestUltraWM15_MetaKeysExactSet(t *testing.T) {
	want := []string{"#uuid", "#type", "#time", "#user_id", "#account_id", "#distinct_id", "_ts"}
	if len(metaKeys) != len(want) {
		t.Errorf("metaKeys has %d entries, want exactly %d: %v", len(metaKeys), len(want), want)
	}
	for _, k := range want {
		if _, ok := metaKeys[k]; !ok {
			t.Errorf("metaKeys missing %q", k)
		}
	}
}

// TestUltraWM15_SplitFields_Partition feeds splitFields a document mixing all
// seven meta keys with business fields — including hash-prefixed non-meta
// fields (#event_name) and near-miss names (_TS, uuid) — and asserts the exact
// two-way partition.
func TestUltraWM15_SplitFields_Partition(t *testing.T) {
	doc := bson.M{
		// All seven meta fields.
		"#uuid":        "u-1",
		"#type":        "user_set",
		"#time":        "2026-06-10 12:00:00",
		"#user_id":     int64(42),
		"#account_id":  "acc-1",
		"#distinct_id": "dis-1",
		"_ts":          int64(1700000000000000000),
		// Business data, including tricky names that must NOT be meta.
		"#event_name": "login", // hash-prefixed but not in metaKeys
		"_TS":         "upper", // metaKeys is case-sensitive
		"uuid":        "bare",  // no hash prefix
		"name":        "Alice",
		"amount":      float64(9.99),
		"vip":         true,
	}

	meta, data := splitFields(doc)

	wantMeta := bson.M{
		"#uuid":        "u-1",
		"#type":        "user_set",
		"#time":        "2026-06-10 12:00:00",
		"#user_id":     int64(42),
		"#account_id":  "acc-1",
		"#distinct_id": "dis-1",
		"_ts":          int64(1700000000000000000),
	}
	wantData := bson.M{
		"#event_name": "login",
		"_TS":         "upper",
		"uuid":        "bare",
		"name":        "Alice",
		"amount":      float64(9.99),
		"vip":         true,
	}
	if !reflect.DeepEqual(meta, wantMeta) {
		t.Errorf("meta = %v, want %v", meta, wantMeta)
	}
	if !reflect.DeepEqual(data, wantData) {
		t.Errorf("data = %v, want %v", data, wantData)
	}
	if got, want := len(meta)+len(data), len(doc); got != want {
		t.Errorf("partition lost/duplicated keys: |meta|+|data| = %d, want %d", got, want)
	}
	for k := range meta {
		if _, dup := data[k]; dup {
			t.Errorf("key %q present in both meta and data", k)
		}
	}
}

// TestUltraWM15_SplitFields_Empty pins that an empty document yields two empty
// (but non-nil, writable) maps.
func TestUltraWM15_SplitFields_Empty(t *testing.T) {
	meta, data := splitFields(bson.M{})
	if meta == nil || len(meta) != 0 {
		t.Errorf("meta = %v, want empty non-nil bson.M", meta)
	}
	if data == nil || len(data) != 0 {
		t.Errorf("data = %v, want empty non-nil bson.M", data)
	}
}

// ---------------------------------------------------------------------------
// WM-7: unknown user_* type falls back to user_set semantics
// ---------------------------------------------------------------------------

// TestUltraWM7_UnknownUserType_FallsBackToUserSetShape asserts that an unknown
// type ("user_weird") produces a write model structurally identical to
// user_set: an upserting *mongo.UpdateOneModel whose filter carries the
// tsGuardOr anti-rollback $or and whose update is a plain {$set: meta+data}.
func TestUltraWM7_UnknownUserType_FallsBackToUserSetShape(t *testing.T) {
	ts := int64(456)
	mkDoc := func() bson.M {
		return bson.M{"#time": "2026-06-10 08:00:00", "_ts": ts, "plan": "gold", "level": int64(3)}
	}

	weird := UserWriteModel("user_weird", 9, mkDoc())
	upd, ok := weird.(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("user_weird model = %T, want *mongo.UpdateOneModel", weird)
	}
	if upd.Upsert == nil || !*upd.Upsert {
		t.Error("user_weird model must be an upsert")
	}
	assertTsGuardFilter(t, upd.Filter, "#user_id", int64(9), ts)

	set := assertPlainUpdate(t, upd.Update)
	setDoc, ok := set["$set"].(bson.M)
	if !ok {
		t.Fatalf("user_weird update missing $set document: %v", set)
	}
	wantSet := bson.M{
		"#user_id": int64(9),
		"#time":    "2026-06-10 08:00:00",
		"_ts":      ts,
		"plan":     "gold",
		"level":    int64(3),
	}
	if !reflect.DeepEqual(setDoc, wantSet) {
		t.Errorf("user_weird $set = %v, want %v", setDoc, wantSet)
	}
	if len(set) != 1 {
		t.Errorf("user_weird update has operators %v, want only $set", set)
	}

	// Byte-for-byte the same filter/update as user_set on an equal input doc.
	ref, ok := UserWriteModel("user_set", 9, mkDoc()).(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("user_set reference model has unexpected type")
	}
	if !reflect.DeepEqual(upd.Filter, ref.Filter) {
		t.Errorf("user_weird filter %v != user_set filter %v", upd.Filter, ref.Filter)
	}
	if !reflect.DeepEqual(upd.Update, ref.Update) {
		t.Errorf("user_weird update %v != user_set update %v", upd.Update, ref.Update)
	}
}

// TestUltraWM7_UnknownUserType_UserSetSemantics_Integration applies a
// "user_weird" operation through BulkWriteOrdered against a throwaway database
// and asserts full user_set semantics on the stored document: upsert-create,
// newer-_ts field merge/overwrite, and older-_ts anti-rollback skip (benign
// duplicate-key treated as no-op, no second document inserted).
func TestUltraWM7_UnknownUserType_UserSetSemantics_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	// The unique #user_id index is what turns an older-_ts upsert into the
	// benign E11000 skip — required for user_set anti-rollback semantics.
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	const userID = int64(7700)
	ts1 := time.Now().UnixNano()

	// 1. Upsert-create via the unknown type.
	m1 := UserWriteModel("user_weird", userID, bson.M{
		"#time": "2026-06-10 09:00:00",
		"_ts":   ts1,
		"plan":  "silver",
		"level": "novice",
	})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{m1}); err != nil {
		t.Fatalf("BulkWriteOrdered create: %v", err)
	}
	var doc bson.M
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": userID}).Decode(&doc); err != nil {
		t.Fatalf("FindOne after create: %v", err)
	}
	if doc["plan"] != "silver" || doc["level"] != "novice" || doc["_ts"] != ts1 {
		t.Errorf("created doc = %v, want plan=silver level=novice _ts=%d", doc, ts1)
	}

	// 2. Newer _ts: $set merge — plan overwritten, untouched field preserved.
	ts2 := ts1 + 1000
	m2 := UserWriteModel("user_weird", userID, bson.M{"_ts": ts2, "plan": "gold"})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{m2}); err != nil {
		t.Fatalf("BulkWriteOrdered newer: %v", err)
	}
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": userID}).Decode(&doc); err != nil {
		t.Fatalf("FindOne after newer write: %v", err)
	}
	if doc["plan"] != "gold" {
		t.Errorf("plan = %v after newer _ts, want gold (user_set overwrites)", doc["plan"])
	}
	if doc["level"] != "novice" {
		t.Errorf("level = %v after newer _ts, want novice ($set merges, does not replace)", doc["level"])
	}
	if doc["_ts"] != ts2 {
		t.Errorf("_ts = %v, want %d", doc["_ts"], ts2)
	}

	// 3. Older _ts: anti-rollback — write skipped, error swallowed as benign.
	ts3 := ts1 - 1000
	m3 := UserWriteModel("user_weird", userID, bson.M{"_ts": ts3, "plan": "bronze"})
	if err := st.BulkWriteOrdered(ctx, st.UserCollection(), []mongo.WriteModel{m3}); err != nil {
		t.Fatalf("BulkWriteOrdered older: %v (older-_ts skip must be a benign no-op)", err)
	}
	if err := st.UserCollection().FindOne(ctx, bson.M{"#user_id": userID}).Decode(&doc); err != nil {
		t.Fatalf("FindOne after older write: %v", err)
	}
	if doc["plan"] != "gold" || doc["_ts"] != ts2 {
		t.Errorf("doc after older _ts = %v, want plan=gold _ts=%d (rollback prevented)", doc, ts2)
	}
	n, err := st.UserCollection().CountDocuments(ctx, bson.M{"#user_id": userID})
	if err != nil {
		t.Fatalf("CountDocuments: %v", err)
	}
	if n != 1 {
		t.Errorf("found %d docs for #user_id=%d, want exactly 1 (skip must not insert)", n, userID)
	}
}

// ---------------------------------------------------------------------------
// WM-11: EventWriteModel injects missing #uuid / _ts
// ---------------------------------------------------------------------------

// TestUltraWM11_EventMetaInjection_Unit asserts that a record lacking both
// #uuid and _ts gets #uuid from the uuid argument and _ts = time.Now().UnixNano(),
// mutated in place, and that the track model carries exactly that document
// under $setOnInsert with filter {#uuid: <arg>}.
func TestUltraWM11_EventMetaInjection_Unit(t *testing.T) {
	doc := bson.M{"#event_name": "login"}

	before := time.Now().UnixNano()
	model := EventWriteModel("track", "uuid-abc", doc)
	after := time.Now().UnixNano()

	// Injection happens in place on the caller's document.
	if doc["#uuid"] != "uuid-abc" {
		t.Errorf(`doc["#uuid"] = %v, want "uuid-abc" (injected from argument)`, doc["#uuid"])
	}
	ts, ok := doc["_ts"].(int64)
	if !ok {
		t.Fatalf(`doc["_ts"] = %T (%v), want int64 UnixNano`, doc["_ts"], doc["_ts"])
	}
	if ts < before || ts > after {
		t.Errorf("_ts = %d, want within [%d, %d] (time.Now().UnixNano())", ts, before, after)
	}

	upd, ok := model.(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("track model = %T, want *mongo.UpdateOneModel", model)
	}
	if upd.Upsert == nil || !*upd.Upsert {
		t.Error("track model must be an upsert")
	}
	if !reflect.DeepEqual(upd.Filter, bson.M{"#uuid": "uuid-abc"}) {
		t.Errorf("filter = %v, want {#uuid: uuid-abc}", upd.Filter)
	}
	set := assertPlainUpdate(t, upd.Update)
	soi, ok := set["$setOnInsert"].(bson.M)
	if !ok {
		t.Fatalf("track update missing $setOnInsert: %v", set)
	}
	want := bson.M{"#event_name": "login", "#uuid": "uuid-abc", "_ts": ts}
	if !reflect.DeepEqual(soi, want) {
		t.Errorf("$setOnInsert = %v, want %v", soi, want)
	}
}

// TestUltraWM11_EventMetaInjection_EmptyAndNil pins the edge cases: an empty
// string #uuid and an explicit nil _ts are both treated as missing and
// replaced; a nil doc is allocated so the model still carries both fields.
func TestUltraWM11_EventMetaInjection_EmptyAndNil(t *testing.T) {
	doc := bson.M{"#uuid": "", "_ts": nil}
	before := time.Now().UnixNano()
	EventWriteModel("track", "u-1", doc)
	after := time.Now().UnixNano()
	if doc["#uuid"] != "u-1" {
		t.Errorf(`empty #uuid: doc["#uuid"] = %v, want "u-1"`, doc["#uuid"])
	}
	if ts, ok := doc["_ts"].(int64); !ok || ts < before || ts > after {
		t.Errorf(`nil _ts: doc["_ts"] = %v (%T), want int64 in [%d, %d]`, doc["_ts"], doc["_ts"], before, after)
	}

	m2, ok := EventWriteModel("track", "u-2", nil).(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("track model for nil doc has unexpected type")
	}
	soi, ok := m2.Update.(bson.M)["$setOnInsert"].(bson.M)
	if !ok {
		t.Fatalf("nil doc: update = %v, want {$setOnInsert: bson.M}", m2.Update)
	}
	if soi["#uuid"] != "u-2" {
		t.Errorf(`nil doc: $setOnInsert["#uuid"] = %v, want "u-2"`, soi["#uuid"])
	}
	if ts, ok := soi["_ts"].(int64); !ok || ts <= 0 {
		t.Errorf(`nil doc: $setOnInsert["_ts"] = %v (%T), want positive int64`, soi["_ts"], soi["_ts"])
	}
}

// TestUltraWM11_ExistingMetaPreserved asserts the converse: a non-empty #uuid
// and a present _ts are NOT overwritten — while the filter still keys on the
// uuid argument (and on the doc's own _ts for the guard).
func TestUltraWM11_ExistingMetaPreserved(t *testing.T) {
	doc := bson.M{"#uuid": "keep-me", "_ts": int64(42), "k": "v"}
	model := EventWriteModel("track_update", "arg-uuid", doc)

	if doc["#uuid"] != "keep-me" {
		t.Errorf(`doc["#uuid"] = %v, want "keep-me" (existing value preserved)`, doc["#uuid"])
	}
	if doc["_ts"] != int64(42) {
		t.Errorf(`doc["_ts"] = %v, want 42 (existing value preserved)`, doc["_ts"])
	}

	upd, ok := model.(*mongo.UpdateOneModel)
	if !ok {
		t.Fatalf("track_update model = %T, want *mongo.UpdateOneModel", model)
	}
	// Filter uses the uuid ARGUMENT, not the doc's #uuid field.
	assertTsGuardFilter(t, upd.Filter, "#uuid", "arg-uuid", int64(42))
}

// TestUltraWM11_EventMetaInjection_Integration round-trips a track record that
// lacks #uuid/_ts through BulkWrite and asserts the stored document carries the
// injected values.
func TestUltraWM11_EventMetaInjection_Integration(t *testing.T) {
	st, _, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	before := time.Now().UnixNano()
	model := EventWriteModel("track", "wm11-uuid", bson.M{
		"#event_name": "purchase",
		"amount":      float64(9.99),
	})
	after := time.Now().UnixNano()

	if err := st.BulkWrite(ctx, st.EventCollection(), []mongo.WriteModel{model}); err != nil {
		t.Fatalf("BulkWrite: %v", err)
	}

	var stored bson.M
	if err := st.EventCollection().FindOne(ctx, bson.M{"#uuid": "wm11-uuid"}).Decode(&stored); err != nil {
		t.Fatalf("FindOne by injected #uuid: %v", err)
	}
	if stored["#event_name"] != "purchase" || stored["amount"] != float64(9.99) {
		t.Errorf("stored doc = %v, want #event_name=purchase amount=9.99", stored)
	}
	ts, ok := stored["_ts"].(int64)
	if !ok {
		t.Fatalf(`stored["_ts"] = %T (%v), want int64`, stored["_ts"], stored["_ts"])
	}
	if ts < before || ts > after {
		t.Errorf("stored _ts = %d, want within [%d, %d]", ts, before, after)
	}
}

// ---------------------------------------------------------------------------
// IDX-2: user collection index set
// ---------------------------------------------------------------------------

// indexSpec is the subset of listIndexes output the IDX assertions need.
type indexSpec struct {
	Name   string `bson:"name"`
	Key    bson.D `bson:"key"`
	Unique bool   `bson:"unique"`
}

// ascending converts a listIndexes key direction (int32/int64/float64
// depending on server/driver) to int for comparison.
func ascending(t *testing.T, v any) int {
	t.Helper()
	switch n := v.(type) {
	case int32:
		return int(n)
	case int64:
		return int(n)
	case float64:
		return int(n)
	case int:
		return n
	default:
		t.Fatalf("index key direction = %T (%v), want a numeric type", v, v)
		return 0
	}
}

// TestUltraIdx2_UserCollectionIndexes asserts that after EnsureIndexes the
// user collection has exactly the indexes ensureUserIndexes declares:
// #user_id (unique), #account_id, #distinct_id and _ts — all single-field
// ascending — plus the implicit _id_.
func TestUltraIdx2_UserCollectionIndexes(t *testing.T) {
	st, db, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()
	if err := st.EnsureIndexes(ctx); err != nil {
		t.Fatalf("EnsureIndexes: %v", err)
	}

	cursor, err := db.Collection("user").Indexes().List(ctx)
	if err != nil {
		t.Fatalf("list user indexes: %v", err)
	}
	var specs []indexSpec
	if err := cursor.All(ctx, &specs); err != nil {
		t.Fatalf("decode user indexes: %v", err)
	}

	byName := make(map[string]indexSpec, len(specs))
	for _, s := range specs {
		byName[s.Name] = s
	}

	wantUnique := map[string]bool{
		"#user_id_1":     true,
		"#account_id_1":  false,
		"#distinct_id_1": false,
		"_ts_1":          false,
	}
	for name, unique := range wantUnique {
		spec, ok := byName[name]
		if !ok {
			t.Errorf("user collection missing index %q (have %v)", name, names(specs))
			continue
		}
		if spec.Unique != unique {
			t.Errorf("index %q unique = %v, want %v", name, spec.Unique, unique)
		}
		if len(spec.Key) != 1 {
			t.Errorf("index %q key = %v, want a single field", name, spec.Key)
			continue
		}
		wantField := name[:len(name)-len("_1")]
		if spec.Key[0].Key != wantField {
			t.Errorf("index %q key field = %q, want %q", name, spec.Key[0].Key, wantField)
		}
		if dir := ascending(t, spec.Key[0].Value); dir != 1 {
			t.Errorf("index %q direction = %d, want 1 (ascending)", name, dir)
		}
	}

	// Exactly the declared set: the four above plus the implicit _id_.
	if _, ok := byName["_id_"]; !ok {
		t.Errorf("user collection missing implicit _id_ index (have %v)", names(specs))
	}
	if len(specs) != len(wantUnique)+1 {
		t.Errorf("user collection has %d indexes %v, want exactly %d", len(specs), names(specs), len(wantUnique)+1)
	}
}

// names extracts index names for error messages.
func names(specs []indexSpec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.Name
	}
	return out
}
