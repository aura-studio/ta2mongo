package logging

import "fmt"

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

// ApplyDefaults fills unset logging options.
func (c *Config) ApplyDefaults() {
	if c.Level == "" {
		c.Level = "info"
	}
	if c.Format == "" {
		c.Format = "text"
	}
}
