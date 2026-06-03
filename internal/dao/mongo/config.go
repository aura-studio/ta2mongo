package mongo

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config configures the MongoDB connection. It is the mongo module's own
// configuration, owned here rather than in the top-level config package, so
// callers depend on this module for its settings. The mapstructure tags let the
// config loader unmarshal the runtime.mongo.* keys straight into it. Retry
// budget (MaxElapsedTime) belongs to the store, not the connection — see
// store.Config.
type Config struct {
	// URI is the MongoDB connection URI (required). The database name is taken
	// from the URI path.
	URI string `mapstructure:"uri"`
	// ConnectTimeout bounds the initial connection handshake. Default 10s.
	ConnectTimeout time.Duration `mapstructure:"connectTimeout"`
	// ServerSelectionTimeout bounds how long the driver waits for a suitable
	// server before failing an operation. Default 30s.
	ServerSelectionTimeout time.Duration `mapstructure:"serverSelectionTimeout"`
}

// Validate checks the MongoDB connection settings. The URI is required for any
// role. A nil receiver is treated as "missing".
func (c *Config) Validate() error {
	if c == nil || c.URI == "" {
		return fmt.Errorf("uri is required")
	}
	return nil
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding works.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".uri", "")
	set(prefix+".connectTimeout", "0s")
	set(prefix+".serverSelectionTimeout", "0s")
}

// ApplyDefaults fills unset MongoDB connection options.
func (c *Config) ApplyDefaults() {
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.ServerSelectionTimeout <= 0 {
		c.ServerSelectionTimeout = 30 * time.Second
	}
}

// MongoDBFromURI extracts the database name from a MongoDB URI path.
// Examples:
//   - mongodb://host:27017/tango => "tango"
//   - mongodb://host:27017       => "tango" (default fallback)
func MongoDBFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse mongo uri: %w", err)
	}
	db := strings.Trim(u.Path, "/")
	if db == "" {
		return "tango", nil
	}
	return db, nil
}
