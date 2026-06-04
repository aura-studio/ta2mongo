// Package parser is the log-line parsing layer. It integrates ThinkingData log
// parsing (talog) and the reporting filter (filter) behind a single Parser
// object, so the service and process layers depend on the parser package rather
// than wiring the talog and filter subpackages individually — mirroring how the
// dao package fronts its store.
package parser

import (
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/parser/talog"
)

// Parser bundles the ThinkingData log parser with the reporting filter. The
// embedded *talog.Parser promotes ParseLine onto Parser; the filter holder is
// reached via Filter so its active filter can be hot-swapped at runtime.
type Parser struct {
	*talog.Parser
	filter *filter.Holder
}

// New builds a Parser with a fresh talog parser and a filter holder initialised
// with flt (which may be nil for a no-op filter that keeps every record).
func New(flt *filter.Filter) *Parser {
	return &Parser{
		Parser: talog.NewParser(),
		filter: filter.NewHolder(flt),
	}
}

// Filter returns the filter holder, whose active filter can be replaced via its
// Store method (e.g. when a remote config update arrives).
func (p *Parser) Filter() *filter.Holder { return p.filter }

// ---------------------------------------------------------------------------
// talog facade
//
// The parser package fronts the parser/talog subpackage so external layers
// (notably the process uploaders) can name the parsed-record model and its
// categories through parser alone, never importing parser/talog directly. The
// reporting filter is reached through Parser.Filter (returning the
// filter.Holder), so the parser/filter subpackage likewise need not be imported
// by consumers.
// ---------------------------------------------------------------------------

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
