package role

import (
	"fmt"

	"rocket-nano/tools/tango/internal/role/daemon"
	"rocket-nano/tools/tango/internal/role/gateway"
)

// Config aggregates role-specific configuration (file key role.*).
type Config struct {
	// Daemon is the daemon role config (file key role.daemon.*).
	Daemon *daemon.Config `mapstructure:"daemon"`
	// Gateway is the HTTP gateway role config (file key role.gateway.*).
	Gateway *gateway.Config `mapstructure:"gateway"`
}

// Validate delegates to the configured role sub-configs.
func (c *Config) Validate() error {
	if c.Daemon != nil {
		if err := c.Daemon.Validate(); err != nil {
			return fmt.Errorf("daemon: %w", err)
		}
	}
	if c.Gateway != nil {
		if err := c.Gateway.Validate(); err != nil {
			return fmt.Errorf("gateway: %w", err)
		}
	}
	return nil
}

// RegisterDefaults cascades default-key registration to the role sub-configs.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	new(daemon.Config).RegisterDefaults(set, prefix+".daemon")
	new(gateway.Config).RegisterDefaults(set, prefix+".gateway")
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Daemon == nil {
		c.Daemon = &daemon.Config{}
	}
	c.Daemon.ApplyDefaults()
	if c.Gateway == nil {
		c.Gateway = &gateway.Config{}
	}
	c.Gateway.ApplyDefaults()
}
