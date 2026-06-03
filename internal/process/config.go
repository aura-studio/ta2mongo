package process

import "rocket-nano/tools/tango/internal/process/pipeline"

// Config configures process-level ingestion behavior.
type Config struct {
	// Pipeline configures batching and parallel write workers.
	Pipeline *pipeline.Config `mapstructure:"pipeline"`
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Pipeline == nil {
		c.Pipeline = &pipeline.Config{}
	}
	c.Pipeline.ApplyDefaults()
}
