package logging

import (
	"fmt"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// FromTree decodes the logging.* branch of t into a Config, applies defaults and
// validates it. The module owns its own section name ("logging"), so callers
// hand it the whole tree and it slices its own branch.
func FromTree(t cfgtree.Tree) (*Config, error) {
	var c Config
	if err := t.Sub("logging").Into(&c); err != nil {
		return nil, fmt.Errorf("logging: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("logging: %w", err)
	}
	return &c, nil
}

// Config configures log output. It is the logging module's own configuration; the
// top-level config package references it by pointer and the loader unmarshals
// logging.* keys into it.
type Config struct {
	// Level is the log verbosity: debug, info, warn, error. Default "info".
	Level string `mapstructure:"level"`
	// Format selects the log encoding: "text" (default) or "json".
	Format string `mapstructure:"format"`
}

// Validate checks the log level and format are recognised (empty = use default).
func (c *Config) Validate() error {
	switch c.Level {
	case "", "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("level must be debug/info/warn/error, got %q", c.Level)
	}
	switch c.Format {
	case "", "text", "json":
	default:
		return fmt.Errorf("format must be text/json, got %q", c.Format)
	}
	return nil
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding works. It owns its leaf keys; the parent owns the
// prefix.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".level", "")
	set(prefix+".format", "")
}

// ApplyDefaults fills unset logging options.
func (c *Config) ApplyDefaults() {
	if c.Level == "" {
		c.Level = "info"
	}
	if c.Format == "" {
		c.Format = "text"
	}
}
