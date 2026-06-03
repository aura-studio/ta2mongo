package config

import (
	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role"
	"rocket-nano/tools/tango/internal/source"
)

// applyDefaults allocates any nil section pointer and delegates to each
// module's own ApplyDefaults, so callers can rely on every section being
// present and defaulted after this runs.
func applyDefaults(c *Config) {
	if c.Logging == nil {
		c.Logging = &logging.Config{}
	}
	c.Logging.ApplyDefaults()

	if c.Dao == nil {
		c.Dao = &dao.Config{}
	}
	c.Dao.ApplyDefaults()

	if c.Parser == nil {
		c.Parser = &parser.Config{}
	}
	c.Parser.ApplyDefaults()

	if c.Source == nil {
		c.Source = &source.Config{}
	}
	c.Source.ApplyDefaults()

	if c.Process == nil {
		c.Process = &process.Config{}
	}
	c.Process.ApplyDefaults()

	if c.Role == nil {
		c.Role = &role.Config{}
	}
	c.Role.ApplyDefaults()
}
