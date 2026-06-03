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
