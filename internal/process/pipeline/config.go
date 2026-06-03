package pipeline

import (
	"fmt"
	"time"
)

// Config configures batching and parallel write workers. It is the pipeline
// module's own configuration; the top-level config package references it by
// pointer and the loader unmarshals the pipeline.* keys into it.
type Config struct {
	// BatchSize is the target number of records per bulk-write flush. The
	// adaptive min/max can be overridden via BatchSizeMin/BatchSizeMax.
	// Default 1000.
	BatchSize int `mapstructure:"batchSize"`
	// BatchSizeMin is the adaptive lower bound for batch sizing. When 0, it is
	// auto-derived as BatchSize/4 (minimum 1).
	BatchSizeMin int `mapstructure:"batchSizeMin"`
	// BatchSizeMax is the adaptive upper bound for batch sizing. When 0, it is
	// auto-derived as BatchSize*2.
	BatchSizeMax int `mapstructure:"batchSizeMax"`
	// BatchWorkers is the number of parallel write workers. Default 2.
	BatchWorkers int `mapstructure:"batchWorkers"`
	// FlushInterval is how often workers flush partial batches. Default 1s.
	FlushInterval time.Duration `mapstructure:"flushInterval"`
	// ChannelBuffer is the per-worker line-channel buffer. Default 0 means
	// "derive as BatchSize*2".
	ChannelBuffer int `mapstructure:"channelBuffer"`
	// DeadLetterCap is the per-worker dead-letter batch capacity. Default 128.
	DeadLetterCap int `mapstructure:"deadLetterCap"`
}

// Validate checks the explicit adaptive batch bounds are consistent with the
// target batch size. A nil receiver is valid. Note ApplyDefaults clamps these,
// so post-default configs always pass; Validate guards pre-default callers.
func (c *Config) Validate() error {
	if c == nil {
		return nil
	}
	if c.BatchSizeMin > 0 && c.BatchSizeMin > c.BatchSize {
		return fmt.Errorf("batchSizeMin (%d) cannot exceed batchSize (%d)", c.BatchSizeMin, c.BatchSize)
	}
	if c.BatchSizeMax > 0 && c.BatchSize > c.BatchSizeMax {
		return fmt.Errorf("batchSize (%d) cannot exceed batchSizeMax (%d)", c.BatchSize, c.BatchSizeMax)
	}
	return nil
}

// ApplyDefaults fills unset pipeline options and clamps explicit batch bounds.
func (c *Config) ApplyDefaults() {
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.BatchSizeMin > 0 && c.BatchSizeMin > c.BatchSize {
		c.BatchSizeMin = c.BatchSize
	}
	if c.BatchSizeMax > 0 && c.BatchSizeMax < c.BatchSize {
		c.BatchSizeMax = c.BatchSize
	}
	if c.BatchWorkers <= 0 {
		c.BatchWorkers = 2
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = time.Second
	}
	if c.DeadLetterCap <= 0 {
		c.DeadLetterCap = 128
	}
}

// MinBatchSize returns the adaptive lower bound for batch sizing. When
// BatchSizeMin is explicitly set (>0) it is used directly (clamped to
// BatchSize); when 0 it is auto-derived as BatchSize/4 (minimum 1).
func (c *Config) MinBatchSize() int {
	if c.BatchSizeMin > 0 {
		if c.BatchSizeMin > c.BatchSize {
			return c.BatchSize
		}
		return c.BatchSizeMin
	}
	v := c.BatchSize / 4
	if v < 1 {
		return 1
	}
	return v
}

// MaxBatchSize returns the adaptive upper bound for batch sizing. When
// BatchSizeMax is explicitly set (>0) it is used directly (clamped to
// BatchSize); when 0 it is auto-derived as BatchSize*2.
func (c *Config) MaxBatchSize() int {
	if c.BatchSizeMax > 0 {
		if c.BatchSizeMax < c.BatchSize {
			return c.BatchSize
		}
		return c.BatchSizeMax
	}
	return c.BatchSize * 2
}

// ChannelSize returns the per-worker channel buffer size. It honours an
// explicit ChannelBuffer when set, otherwise derives BatchSize*2.
func (c *Config) ChannelSize() int {
	if c.ChannelBuffer > 0 {
		return c.ChannelBuffer
	}
	return c.BatchSize * 2
}
