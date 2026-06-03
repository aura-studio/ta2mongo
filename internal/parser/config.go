package parser

import "rocket-nano/tools/tango/internal/parser/filter"

// Config composes parser-layer configuration. Filter owns the reporting filter
// rules; talog currently has no tunable configuration.
type Config struct {
	Filter *filter.Config `mapstructure:"filter"`
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
