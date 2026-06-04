package parser

import (
	"fmt"

	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/parser/filter"
)

// FromTree decodes the parser.* branch of t into a Config, applies defaults and
// validates it (which compiles the parser.filter.* expressions).
func FromTree(t cfgtree.Tree) (*Config, error) {
	var c Config
	if err := t.Sub("parser").Into(&c); err != nil {
		return nil, fmt.Errorf("parser: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("parser: %w", err)
	}
	return &c, nil
}

// Config composes parser-layer configuration. Filter owns the reporting filter
// rules; talog currently has no tunable configuration.
type Config struct {
	Filter *filter.Config `mapstructure:"filter"`
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Filter == nil {
		c.Filter = &filter.Config{}
	}
}

// Validate delegates to the filter sub-config (the only parser knob with rules).
func (c *Config) Validate() error {
	if err := c.Filter.Validate(); err != nil {
		return fmt.Errorf("filter: %w", err)
	}
	return nil
}

// RegisterDefaults cascades default-key registration to the filter sub-config.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	new(filter.Config).RegisterDefaults(set, prefix+".filter")
}

// Build compiles the configured filter and returns a ready parser.
func (c *Config) Build() (*Parser, error) {
	var fc *filter.Config
	if c != nil {
		fc = c.Filter
	}
	flt, err := fc.Build()
	if err != nil {
		return nil, err
	}
	return New(flt), nil
}
