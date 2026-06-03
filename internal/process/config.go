package process

import (
	"rocket-nano/tools/tango/internal/process/batch"
	"rocket-nano/tools/tango/internal/process/pipeline"
)

// Config configures process-level upload behavior across the three strategies.
type Config struct {
	// BatchSize is the bulk-write flush size for the single/batch strategies.
	// Default 1000.
	BatchSize int `mapstructure:"batchSize"`
	// Pipeline configures batching and parallel write workers (pipeline mode).
	Pipeline *pipeline.Config `mapstructure:"pipeline"`
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.BatchSize <= 0 {
		c.BatchSize = batch.DefaultBatchSize
	}
	if c.Pipeline == nil {
		c.Pipeline = &pipeline.Config{}
	}
	c.Pipeline.ApplyDefaults()
}
