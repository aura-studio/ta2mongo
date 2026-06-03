package dao

import (
	"fmt"

	"rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
)

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
