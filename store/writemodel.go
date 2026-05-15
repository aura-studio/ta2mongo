package store

import (
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// ---------------------------------------------------------------------------
// Meta / data field separation (used by user write models)
// ---------------------------------------------------------------------------

// metaKeys are TA identifier and timestamp fields managed separately from
// business data fields. _ts is the ingestion timestamp used for ordering.
var metaKeys = map[string]struct{}{
	"#uuid":        {},
	"#type":        {},
	"#time":        {},
	"#user_id":     {},
	"#account_id":  {},
	"#distinct_id": {},
	"_ts":          {},
}

// splitFields separates a document into meta (identifier/timestamp) fields
// and data (business property) fields.
func splitFields(doc bson.M) (meta bson.M, data bson.M) {
	meta = make(bson.M, 6)
	data = make(bson.M, len(doc))
	for k, v := range doc {
		if _, ok := metaKeys[k]; ok {
			meta[k] = v
		} else {
			data[k] = v
		}
	}
	return meta, data
}

// tsCondSet builds a pipeline $set stage that conditionally sets fields only
// when the incoming _ts >= the existing document's _ts. This prevents older
// records from overwriting newer ones.
//
// For the _ts field itself, $max is used to ensure it only advances forward.
// For all other fields, $cond checks `incoming_ts >= existing_ts` before updating.
//
// On insert (upsert with no existing document), $ifNull ensures the condition
// evaluates to true so all fields are written.
func tsCondSet(ts any, fields bson.M) bson.M {
	stage := bson.M{
		// _ts only moves forward.
		"_ts": bson.M{"$max": bson.A{"$_ts", ts}},
	}

	// The condition: incoming _ts >= existing _ts (treat missing as 0).
	cond := bson.M{"$gte": bson.A{ts, bson.M{"$ifNull": bson.A{"$_ts", 0}}}}

	for k, v := range fields {
		if k == "_ts" {
			continue // handled above
		}
		stage[k] = bson.M{
			"$cond": bson.M{
				"if":   cond,
				"then": v,
				// Preserve existing value; if field doesn't exist yet, use the new value
				// (covers the upsert-insert case where the field is absent).
				"else": bson.M{"$ifNull": bson.A{"$" + k, v}},
			},
		}
	}
	return stage
}

// tsCondUnset builds a pipeline stage that conditionally removes fields only
// when the incoming _ts >= the existing document's _ts.
func tsCondUnset(ts any, keys []string) bson.A {
	cond := bson.M{"$gte": bson.A{ts, bson.M{"$ifNull": bson.A{"$_ts", 0}}}}

	// We use $set to replace each target field with $$REMOVE when condition is met.
	stage := bson.M{
		"_ts": bson.M{"$max": bson.A{"$_ts", ts}},
	}
	for _, k := range keys {
		stage[k] = bson.M{
			"$cond": bson.M{
				"if":   cond,
				"then": "$$REMOVE",
				"else": "$" + k,
			},
		}
	}
	return bson.A{bson.M{"$set": stage}}
}

// metaMaxUpdate builds a traditional (non-pipeline) update document that uses
// $max for _ts and #time, and $set for other identity meta fields.
// This is used by operations where data fields have their own semantics
// ($inc, $push, $addToSet, $setOnInsert) and only the meta timestamps need protection.
func metaMaxUpdate(meta bson.M) bson.M {
	update := bson.M{}
	setFields := make(bson.M, len(meta))
	maxFields := make(bson.M, 2)

	for k, v := range meta {
		switch k {
		case "_ts":
			maxFields["_ts"] = v
		default:
			setFields[k] = v
		}
	}

	if len(setFields) > 0 {
		update["$set"] = setFields
	}
	if len(maxFields) > 0 {
		update["$max"] = maxFields
	}
	return update
}

// ---------------------------------------------------------------------------
// User write models  (ThinkingData user_* semantics)
// ---------------------------------------------------------------------------

// mergeFields merges meta and data fields into a single bson.M.
func mergeFields(meta, data bson.M) bson.M {
	all := make(bson.M, len(meta)+len(data))
	for k, v := range meta {
		all[k] = v
	}
	for k, v := range data {
		all[k] = v
	}
	return all
}

// upsertWithOperator builds an upsert UpdateOneModel that protects meta
// timestamps via $max/$set and applies the given MongoDB operator to the data.
func upsertWithOperator(filter, meta bson.M, op string, data bson.M) mongo.WriteModel {
	update := metaMaxUpdate(meta)
	if len(data) > 0 {
		update[op] = data
	}
	return mongo.NewUpdateOneModel().
		SetFilter(filter).
		SetUpdate(update).
		SetUpsert(true)
}

// UserWriteModel builds the appropriate MongoDB write model for a user operation.
// The user document is keyed by #user_id (the resolved TA user ID) so that all
// user_* operations for the same user are applied to a single document.
//
// Timestamp-based ordering: all user operations use _ts (ingestion timestamp)
// to prevent out-of-order writes. Older records (lower _ts) cannot overwrite
// fields that were already set by newer records (higher _ts).
func UserWriteModel(typ string, userID int64, doc bson.M) mongo.WriteModel {
	filter := bson.M{"#user_id": userID}
	doc["#user_id"] = userID
	meta, data := splitFields(doc)
	ts := meta["_ts"]

	switch typ {
	case "user_set":
		// Overwrite user properties, but only if this record is not older than
		// what's already stored. Uses aggregation pipeline update (MongoDB 4.2+).
		pipeline := bson.A{bson.M{"$set": tsCondSet(ts, mergeFields(meta, data))}}
		return mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(pipeline).
			SetUpsert(true)

	case "user_setOnce":
		// Set properties only on insert; existing values are not overwritten.
		// Meta timestamps use $max to only advance forward.
		return upsertWithOperator(filter, meta, "$setOnInsert", data)

	case "user_add":
		// Increment numeric user properties. $inc is commutative so order doesn't
		// matter for data fields. Meta timestamps use $max.
		return upsertWithOperator(filter, meta, "$inc", data)

	case "user_unset":
		// Remove specified user properties, but only if this record is not older
		// than what's already stored. Uses aggregation pipeline.
		keys := make([]string, 0, len(data))
		for k := range data {
			keys = append(keys, k)
		}
		// Also update meta fields conditionally.
		metaStage := tsCondSet(ts, meta)
		pipeline := tsCondUnset(ts, keys)
		// Merge meta into the first stage.
		firstStage := pipeline[0].(bson.M)["$set"].(bson.M)
		for k, v := range metaStage {
			if k == "_ts" {
				continue // already in tsCondUnset
			}
			firstStage[k] = v
		}
		return mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(pipeline).
			SetUpsert(true)

	case "user_del":
		// Delete the entire user record. No timestamp check needed.
		return mongo.NewDeleteOneModel().SetFilter(filter)

	case "user_append":
		// Append values to array-type user properties. $push is additive,
		// order doesn't affect correctness. Meta timestamps use $max.
		return upsertWithOperator(filter, meta, "$push", toEachFields(data))

	case "user_uniq_append":
		// Append values to array-type properties with deduplication.
		// $addToSet is idempotent. Meta timestamps use $max.
		return upsertWithOperator(filter, meta, "$addToSet", toEachFields(data))

	default:
		// Fallback: same as user_set with timestamp protection.
		pipeline := bson.A{bson.M{"$set": tsCondSet(ts, mergeFields(meta, data))}}
		return mongo.NewUpdateOneModel().
			SetFilter(filter).
			SetUpdate(pipeline).
			SetUpsert(true)
	}
}

// toEachFields wraps values in $each for $push/$addToSet operations.
func toEachFields(data bson.M) bson.M {
	result := make(bson.M, len(data))
	for k, v := range data {
		switch arr := v.(type) {
		case []any:
			result[k] = bson.M{"$each": arr}
		default:
			result[k] = bson.M{"$each": []any{v}}
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Event write models  (ThinkingData track* semantics)
// ---------------------------------------------------------------------------

// trackTsCondSetNoOp builds a $set stage for `track_update` with strict
// no-op semantics for older records.
//
// Behavior for each non-_ts field:
// - if incoming _ts >= existing _ts: set the field to incoming value
// - else (incoming is older):
//   - if the field exists in existing doc: keep existing value
//   - if the field is missing in existing doc: do NOT create it
//
// We achieve the "do not create when missing" via $$REMOVE in the else
// branch when the field is null/missing.
func trackTsCondSetNoOp(ts any, fields bson.M) bson.M {
	stage := bson.M{
		// _ts only moves forward.
		"_ts": bson.M{"$max": bson.A{"$_ts", ts}},
	}

	cond := bson.M{"$gte": bson.A{ts, bson.M{"$ifNull": bson.A{"$_ts", 0}}}}

	for k, v := range fields {
		if k == "_ts" {
			continue
		}

		stage[k] = bson.M{
			"$cond": bson.M{
				"if":   cond,
				"then": v,
				"else": bson.M{
					"$ifNull": bson.A{
						"$" + k,
						"$$REMOVE",
					},
				},
			},
		}
	}

	return stage
}

// trackTsCondSetPipeline builds an update pipeline for `track_update` with
// strict no-op semantics for older records.
func trackTsCondSetPipeline(ts any, doc bson.M) bson.A {
	return bson.A{bson.M{"$set": trackTsCondSetNoOp(ts, doc)}}
}

// trackTsCondReplacePipeline builds an update pipeline for `track_overwrite`.
// It performs a real "full replace" when incoming `_ts` >= existing `_ts`;
// otherwise it keeps the existing document unchanged.
func trackTsCondReplacePipeline(ts any, doc bson.M) bson.A {
	cond := bson.M{
		"$gte": bson.A{
			ts,
			bson.M{"$ifNull": bson.A{"$_ts", 0}},
		},
	}

	// $replaceWith takes an expression that returns the entire replacement
	// document. We use $literal to treat the provided `doc` map as a literal
	// object rather than field-path expressions.
	return bson.A{bson.M{
		"$replaceWith": bson.M{
			"$cond": bson.A{
				cond,
				bson.M{"$literal": doc},
				"$$ROOT",
			},
		},
	}}
}

// EventWriteModel builds the appropriate MongoDB write model for an event operation.
func EventWriteModel(typ, uuid string, doc bson.M) mongo.WriteModel {
	// Ensure required meta fields are present for ordering and upsert.
	if doc == nil {
		doc = bson.M{}
	}
	if v, ok := doc["#uuid"].(string); !ok || v == "" {
		doc["#uuid"] = uuid
	}
	ts, ok := doc["_ts"]
	if !ok || ts == nil {
		ts = time.Now().UnixNano()
		doc["_ts"] = ts
	}

	switch typ {
	case "track":
		// Simple insert of the event document.
		return mongo.NewInsertOneModel().SetDocument(doc)

	case "track_update":
		// Field-level update with per-#uuid `_ts` ordering protection.
		return mongo.NewUpdateOneModel().
			SetFilter(bson.M{"#uuid": uuid}).
			SetUpdate(trackTsCondSetPipeline(ts, doc)).
			SetUpsert(true)

	case "track_overwrite":
		// Full replacement with per-#uuid `_ts` ordering protection.
		return mongo.NewUpdateOneModel().
			SetFilter(bson.M{"#uuid": uuid}).
			SetUpdate(trackTsCondReplacePipeline(ts, doc)).
			SetUpsert(true)

	default:
		return mongo.NewInsertOneModel().SetDocument(doc)
	}
}

// ---------------------------------------------------------------------------
// Dead letter write model
// ---------------------------------------------------------------------------

// DeadLetterModel creates an insert model for a failed log line.
func DeadLetterModel(line string, parseErr error) mongo.WriteModel {
	errMsg := ""
	if parseErr != nil {
		errMsg = parseErr.Error()
	}
	doc := bson.M{
		"_ts":   time.Now().UnixNano(),
		"line":  line,
		"error": errMsg,
	}
	return mongo.NewInsertOneModel().SetDocument(doc)
}
