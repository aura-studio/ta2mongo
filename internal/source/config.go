package source

import (
	"fmt"

	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/source/file"
	"github.com/aura-studio/tango/internal/source/mem"
	"github.com/aura-studio/tango/internal/source/tailer"
)

// FromTree decodes the source.* branch of t into a Config, applies defaults and
// validates it.
func FromTree(t cfgtree.Tree) (*Config, error) {
	var c Config
	if err := t.Sub("source").Into(&c); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	return &c, nil
}

// Config aggregates the data-source configurations, fronting the source
// subpackages so the file schema key (source.*) maps to the package path. The
// tailer, file and mem sources carry configuration; httpbody/stdin take their
// input at call time and need none.
type Config struct {
	// Tailer configures the file-tailing source (file key source.tailer.*).
	Tailer *tailer.Config `mapstructure:"tailer"`
	// File configures the one-shot file-import source (config key
	// source.file.*): explicit file paths, no glob, no directories.
	File *file.Config `mapstructure:"file"`
	// Mem configures the in-memory relay source (config key source.mem.*): the
	// buffer bounding how far a producer (e.g. the backfill fetcher) may run
	// ahead of the draining pipeline. Engine.RunBackfill sizes its relay from it.
	Mem *mem.Config `mapstructure:"mem"`
}

// Validate delegates to the configured source sub-configs.
func (c *Config) Validate() error {
	if c.Tailer != nil {
		if err := c.Tailer.Validate(); err != nil {
			return fmt.Errorf("tailer: %w", err)
		}
	}
	return nil
}

// RegisterDefaults cascades default-key registration to the source sub-configs.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	new(tailer.Config).RegisterDefaults(set, prefix+".tailer")
	new(file.Config).RegisterDefaults(set, prefix+".file")
	new(mem.Config).RegisterDefaults(set, prefix+".mem")
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Tailer == nil {
		c.Tailer = &tailer.Config{}
	}
	c.Tailer.ApplyDefaults()
	if c.File == nil {
		c.File = &file.Config{}
	}
	c.File.ApplyDefaults()
	if c.Mem == nil {
		c.Mem = &mem.Config{}
	}
	c.Mem.ApplyDefaults()
}
