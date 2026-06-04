package dao

import (
	"fmt"

	"rocket-nano/tools/tango/internal/cfgtree"
	"rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
)

// FromTree decodes the dao.* branch of t into a Config, applies defaults and
// validates it (which requires dao.mongo.uri).
func FromTree(t cfgtree.Tree) (*Config, error) {
	var c Config
	if err := t.Sub("dao").Into(&c); err != nil {
		return nil, fmt.Errorf("dao: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("dao: %w", err)
	}
	return &c, nil
}

// Config composes the data-access configuration owned by dao. Mongo contains
// connection settings, while Store contains persistence/write behaviour.
type Config struct {
	Mongo *mongo.Config `mapstructure:"mongo"`
	Store *store.Config `mapstructure:"store"`
}

// Validate delegates to the data-access sub-configs (the store has no
// validation rules of its own). A nil Mongo is treated as missing.
func (c *Config) Validate() error {
	m := c.Mongo
	if m == nil {
		m = &mongo.Config{}
	}
	if err := m.Validate(); err != nil {
		return fmt.Errorf("mongo: %w", err)
	}
	return nil
}

// RegisterDefaults cascades default-key registration to the sub-configs.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	new(mongo.Config).RegisterDefaults(set, prefix+".mongo")
	new(store.Config).RegisterDefaults(set, prefix+".store")
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Mongo == nil {
		c.Mongo = &mongo.Config{}
	}
	c.Mongo.ApplyDefaults()
	if c.Store == nil {
		c.Store = &store.Config{}
	}
	c.Store.ApplyDefaults()
}
