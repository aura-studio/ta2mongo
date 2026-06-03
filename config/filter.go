package config

import "rocket-nano/tools/tango/internal/core/filter"

// BuildFilter compiles the configured filter expressions. Validate must have
// been called first; this method is intended to be invoked by runtime
// components (the report service / upload paths) that need a ready-to-use filter.
func (c *Config) BuildFilter() (*filter.Filter, error) {
	return filter.New(c.Filter.Include, c.Filter.Exclude)
}

// BuildBackfillFilter compiles the backfill selection filter into a local
// (in-process) filter used as a safety net behind the SQL pushdown. It is
// driven by BackfillFilter (table + events), never by the reporting Filter.
func (c *Config) BuildBackfillFilter() (*filter.Filter, error) {
	return filter.New(c.BackfillFilter.IncludeExprs(), c.BackfillFilter.Exclude)
}

// BackfillWhere renders the backfill filter as a Presto WHERE-clause body
// (no leading WHERE), pushing the event-name predicate down to the TA OpenAPI.
func (c *Config) BackfillWhere() (string, error) {
	return filter.CompileToSQL(c.BackfillFilter.IncludeExprs(), c.BackfillFilter.Exclude)
}
