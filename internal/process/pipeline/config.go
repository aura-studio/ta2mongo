package pipeline

import "time"

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
