package parser

import (
	"fmt"

	"rocket-nano/tools/tango/internal/parser/filter"
)

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
