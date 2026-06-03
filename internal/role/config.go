// Package role is the runtime-role collection (daemon / gateway / cli / api).
// role.Config aggregates the per-role configurations that are file-driven so
// their schema keys map to the package path (e.g. role.gateway.*). The api and
// cli roles take their settings programmatically / from shared top-level
// sections and carry no role-specific file config.
package role

import (
	"fmt"

	"rocket-nano/tools/tango/internal/role/gateway"
)

// Config aggregates role-specific configuration (file key role.*).
type Config struct {
	// Gateway is the HTTP gateway role config (file key role.gateway.*).
	Gateway *gateway.Config `mapstructure:"gateway"`
}

// Validate delegates to the configured role sub-configs.
func (c *Config) Validate() error {
	if c.Gateway != nil {
		if err := c.Gateway.Validate(); err != nil {
			return fmt.Errorf("gateway: %w", err)
		}
	}
	return nil
}

// RegisterDefaults cascades default-key registration to the role sub-configs.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	new(gateway.Config).RegisterDefaults(set, prefix+".gateway")
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Gateway == nil {
		c.Gateway = &gateway.Config{}
	}
	c.Gateway.ApplyDefaults()
}
