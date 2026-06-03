package config

import "rocket-nano/tools/tango/internal/parser"

// BuildParser compiles parser-layer config and returns a ready parser.
func (c *Config) BuildParser() (*parser.Parser, error) {
	return c.Parser.Build()
}
