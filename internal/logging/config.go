package logging

// Config configures log output. It is the logging module's own configuration; the
// top-level config package references it by pointer and the role loader
// unmarshals runtime.logging.* keys into it.
type Config struct {
	// Level is the log verbosity: debug, info, warn, error. Default "info".
	Level string `mapstructure:"level"`
	// Format selects the log encoding: "text" (default) or "json".
	Format string `mapstructure:"format"`
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
