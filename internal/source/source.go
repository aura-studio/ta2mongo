// Package source is the log-line data source layer. It integrates ThinkingData
// log parsing (talog) and the reporting filter (filter) behind a single Source
// object, so the service and process layers depend on the source package rather
// than wiring the parser and filter subpackages individually — mirroring how
// the dao package fronts its store.
package source

import (
	"rocket-nano/tools/tango/internal/source/filter"
	"rocket-nano/tools/tango/internal/source/talog"
)

// Source bundles the ThinkingData log parser with the reporting filter. The
// embedded *talog.Parser promotes ParseLine onto Source; the filter holder is
// reached via Filter so its active filter can be hot-swapped at runtime.
type Source struct {
	*talog.Parser
	filter *filter.Holder
}

// New builds a Source with a fresh parser and a filter holder initialised with
// flt (which may be nil for a no-op filter that keeps every record).
func New(flt *filter.Filter) *Source {
	return &Source{
		Parser: talog.NewParser(),
		filter: filter.NewHolder(flt),
	}
}

// Filter returns the filter holder, whose active filter can be replaced via its
// Store method (e.g. when a remote config update arrives).
func (s *Source) Filter() *filter.Holder { return s.filter }
