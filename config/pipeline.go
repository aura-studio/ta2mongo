package config

// BatchSizeMin returns the adaptive lower bound for batch sizing.
// When BatchSizeMin is explicitly set (>0), it is used directly (clamped to BatchSize).
// When BatchSizeMin is 0, it is auto-derived as BatchSize/4 (minimum 1).
func (c Config) BatchSizeMin() int {
	if c.Pipeline.BatchSizeMin > 0 {
		if c.Pipeline.BatchSizeMin > c.Pipeline.BatchSize {
			return c.Pipeline.BatchSize
		}
		return c.Pipeline.BatchSizeMin
	}
	v := c.Pipeline.BatchSize / 4
	if v < 1 {
		return 1
	}
	return v
}

// BatchSizeMax returns the adaptive upper bound for batch sizing.
// When BatchSizeMax is explicitly set (>0), it is used directly (clamped to BatchSize).
// When BatchSizeMax is 0, it is auto-derived as BatchSize*2.
func (c Config) BatchSizeMax() int {
	if c.Pipeline.BatchSizeMax > 0 {
		if c.Pipeline.BatchSizeMax < c.Pipeline.BatchSize {
			return c.Pipeline.BatchSize
		}
		return c.Pipeline.BatchSizeMax
	}
	return c.Pipeline.BatchSize * 2
}

// BatchChannelSize returns the per-worker channel buffer size. It honours an
// explicit pipeline.channelBuffer when set, otherwise derives BatchSize*2.
func (c Config) BatchChannelSize() int {
	if c.Pipeline.ChannelBuffer > 0 {
		return c.Pipeline.ChannelBuffer
	}
	return c.Pipeline.BatchSize * 2
}
