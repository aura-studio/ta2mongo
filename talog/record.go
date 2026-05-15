// Package talog parses and validates ThinkingData JSON log lines.
package talog

import "strings"

// EnvelopeKeys are the field names that may contain a nested TA JSON payload
// when the log line uses an envelope format (e.g. structured logging wrappers).
var EnvelopeKeys = []string{"msg", "message", "log"}

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
	if IsUserType(r.Type) {
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
