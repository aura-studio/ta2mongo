package source

import (
	"fmt"

	"rocket-nano/tools/tango/internal/cfgtree"
	"rocket-nano/tools/tango/internal/source/tailer"
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
// subpackages so the file schema key (source.*) maps to the package path. Only
// the file-tailing source carries configuration today; httpbody/stdin take
// their input at call time and need none.
type Config struct {
	// Tailer configures the file-tailing source (file key source.tailer.*).
	Tailer *tailer.Config `mapstructure:"tailer"`
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
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Tailer == nil {
		c.Tailer = &tailer.Config{}
	}
	c.Tailer.ApplyDefaults()
}
