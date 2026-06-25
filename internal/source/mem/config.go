package mem

// Config configures the in-memory relay source (config key source.mem.*). It is
// the mem module's own configuration; the source package's aggregate Config
// references it by pointer and the loader unmarshals the source.mem.* keys into
// it. It mirrors source/file's Config — file is the on-disk finite source, mem
// the in-memory one — but the relay's only knob is its buffer size.
type Config struct {
	// BufferSize is the relay channel's capacity in lines (config key
	// source.mem.bufferSize): the single producer (e.g. the backfill fetcher)
	// may push up to this many lines ahead of the draining pipeline before Push
	// blocks (backpressure). A non-positive value falls back to defaultBuffer.
	BufferSize int `mapstructure:"bufferSize"`
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding works.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".bufferSize", 0)
}

// ApplyDefaults fills unset relay options.
func (c *Config) ApplyDefaults() {
	if c.BufferSize <= 0 {
		c.BufferSize = defaultBuffer
	}
}
