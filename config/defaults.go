package config

import (
	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// applyDefaults fills in zero-value fields with sensible defaults, allocating
// any section pointer that is nil so callers can rely on every section being
// present after this runs.
func applyDefaults(c *Config) {
	if c.Role == nil {
		c.Role = &role.Config{}
	}
	c.Role.ApplyDefaults()

	if c.Runtime == nil {
		c.Runtime = &RuntimeConfig{}
	}
	c.Runtime.ApplyDefaults()

	if c.Dao == nil {
		c.Dao = &dao.Config{}
	}
	c.Dao.ApplyDefaults()

	if c.Source == nil {
		c.Source = &tailer.Config{}
	}
	c.Source.ApplyDefaults()

	if c.Process == nil {
		c.Process = &process.Config{}
	}
	c.Process.ApplyDefaults()

	if c.Parser == nil {
		c.Parser = &parser.Config{}
	}
	c.Parser.ApplyDefaults()
}
