package dao

import (
	"fmt"

	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/dao/store"
)

// URIConfigured reports whether dao.mongo.uri is present (non-empty) in the
// resolved tree — file, TANGO_DAO_MONGO_URI env, or flag — WITHOUT validating
// the rest of the dao config. Long-running roles (daemon / gateway) use it to
// enter a degraded idle / unavailable mode instead of failing startup when the
// URI is intentionally left empty; FromTree below still rejects an empty URI
// for every caller that actually needs a connection.
func URIConfigured(t cfgtree.Tree) bool {
	var c Config
	if err := t.Sub("dao").Into(&c); err != nil {
		return false
	}
	return c.Mongo != nil && c.Mongo.URI != ""
}

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
