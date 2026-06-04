package parser

import (
	"rocket-nano/tools/tango/internal/parser/talog"
)

// This file fronts the parser/talog subpackage so external layers (notably the
// process uploaders) can name the parsed-record model and its categories through
// the parser package alone, never importing parser/talog directly. The reporting
// filter is reached through Parser.Filter (returning the filter.Holder), so the
// parser/filter subpackage likewise need not be imported by consumers.

// Record is a validated ThinkingData log record produced by Parser.ParseLine.
// It is an alias for talog.Record.
type Record = talog.Record

// RecordCategory classifies a record into its storage target (user vs event).
// It is an alias for talog.RecordCategory.
type RecordCategory = talog.RecordCategory

const (
	// CategoryUser marks user_* records (written to the user collection).
	CategoryUser = talog.CategoryUser
	// CategoryEvent marks track* records (written to the event collection).
	CategoryEvent = talog.CategoryEvent
)

// EnvelopeKeys are the field names that may wrap a nested TA JSON payload (e.g.
// "msg", "message", "log"). Re-exported from talog for routing-key extraction.
var EnvelopeKeys = talog.EnvelopeKeys
