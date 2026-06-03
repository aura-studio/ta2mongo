package process

import (
	"fmt"

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

// Validate delegates to the pipeline sub-config.
func (c *Config) Validate() error {
	if err := c.Pipeline.Validate(); err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}
	return nil
}

// RegisterDefaults registers the process keys and cascades to the pipeline
// sub-config.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".batchSize", 0)
	new(pipeline.Config).RegisterDefaults(set, prefix+".pipeline")
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
