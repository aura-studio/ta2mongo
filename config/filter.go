package config

import "rocket-nano/tools/tango/internal/source/filter"

// BuildFilter compiles the configured filter expressions. Validate must have
// been called first; this method is intended to be invoked by runtime
// components (the report service / upload paths) that need a ready-to-use filter.
func (c *Config) BuildFilter() (*filter.Filter, error) {
	return filter.New(c.Filter.Include, c.Filter.Exclude)
}
