package process

import (
	"fmt"

	"rocket-nano/tools/tango/internal/process/batch"
	"rocket-nano/tools/tango/internal/process/pipeline"
)

// Config configures process-level upload behavior across the three strategies.
type Config struct {
	// Mode selects the upload strategy: single, batch, or pipeline. Default
	// batch. All roles that ingest finite sources (api / cli / gateway) use
	// this field instead of accepting a separate per-call mode.
	Mode string `mapstructure:"mode"`
	// BatchSize is the bulk-write flush size for the single/batch strategies.
	// Default 1000.
	BatchSize int `mapstructure:"batchSize"`
	// Pipeline configures batching and parallel write workers (pipeline mode).
	Pipeline *pipeline.Config `mapstructure:"pipeline"`
}

// Validate delegates to the pipeline sub-config.
func (c *Config) Validate() error {
	if _, err := c.ModeValue(); err != nil {
		return err
	}
	if err := c.Pipeline.Validate(); err != nil {
		return fmt.Errorf("pipeline: %w", err)
	}
	return nil
}

// ModeValue validates and returns the configured upload strategy.
func (c *Config) ModeValue() (Mode, error) {
	if c == nil || c.Mode == "" {
		return ModeBatch, nil
	}
	return ParseMode(c.Mode)
}

// RegisterDefaults registers the process keys and cascades to the pipeline
// sub-config.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".mode", "")
	set(prefix+".batchSize", 0)
	new(pipeline.Config).RegisterDefaults(set, prefix+".pipeline")
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = string(ModeBatch)
	}
	if c.BatchSize <= 0 {
		c.BatchSize = batch.DefaultBatchSize
	}
	if c.Pipeline == nil {
		c.Pipeline = &pipeline.Config{}
	}
	c.Pipeline.ApplyDefaults()
}
