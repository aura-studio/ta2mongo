// Package talog parses and validates ThinkingData JSON log lines.
package talog

import "strings"

// Record is a validated ThinkingData log record ready for storage.
type Record struct {
	// Type is the TA operation type (e.g. "user_set", "track").
	Type string
	// UUID is the record's unique identifier (#uuid).
	UUID string
	// AccountID is the user's login ID (#account_id), may be empty.
	AccountID string
	// DistinctID is the user's visitor ID (#distinct_id), may be empty.
	DistinctID string
	// Doc is the flattened document for MongoDB insertion/update.
	// "properties" sub-fields are promoted to the root level.
	Doc map[string]any
}

// RecordCategory classifies a TA record type into a storage target.
type RecordCategory int

const (
	CategoryUser  RecordCategory = iota // user_* types
	CategoryEvent                       // track* types
)

// Category returns the storage category for a record.
func (r *Record) Category() RecordCategory {
	if strings.HasPrefix(r.Type, "user_") {
		return CategoryUser
	}
	return CategoryEvent
}

// IsUserType returns true for user_* operation types.
func IsUserType(typ string) bool {
	return strings.HasPrefix(typ, "user_")
}

// IsEventType returns true for track* operation types.
func IsEventType(typ string) bool {
	return strings.HasPrefix(typ, "track")
}

// Known TA operation types.
var (
	// _ts protection semantics for user_* types:
	// - user_set / user_unset:
	//   Use MongoDB aggregation pipeline with conditional update/removal.
	//   Older records (incoming _ts < existing _ts) won't overwrite/remove fields.
	// - user_setOnce / user_add / user_append / user_uniq_append:
	//   Protect meta timestamps with $max, while data update semantics follow their operators
	//   ($setOnInsert / $inc / $push / $addToSet).
	//   Ordering guarantee is therefore weaker than user_set/user_unset.
	// - user_del:
	//   Delete the whole user record; no _ts check is performed.
	UserTypes = map[string]struct{}{
		"user_set":         {}, // conditional field overwrite guarded by _ts
		"user_unset":       {}, // conditional field removal guarded by _ts
		"user_setOnce":     {}, // data written only on insert; meta _ts protected by $max
		"user_add":         {}, // meta _ts protected by $max; data uses $inc (commutative)
		"user_append":      {}, // meta _ts protected by $max; data uses $push (array order may vary)
		"user_uniq_append": {}, // meta _ts protected by $max; data uses $addToSet (idempotent)
		"user_del":         {}, // no _ts check; record deletion
	}

	EventTypes = map[string]struct{}{
		"track":           {},
		"track_update":    {},
		"track_overwrite": {},
	}
)
