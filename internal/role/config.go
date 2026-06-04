package role

import (
	"fmt"

	"rocket-nano/tools/tango/internal/cfgtree"
	"rocket-nano/tools/tango/internal/role/daemon"
	"rocket-nano/tools/tango/internal/role/gateway"
)

// FromTree decodes the role.* branch of t into a Config, applies defaults and
// validates it (which checks role.mode). The runtime role is then selected from
// Config.Mode via Get.
func FromTree(t cfgtree.Tree) (*Config, error) {
	var c Config
	if err := t.Sub("role").Into(&c); err != nil {
		return nil, fmt.Errorf("role: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("role: %w", err)
	}
	return &c, nil
}

// DefaultMode is the runtime role used when role.mode is unset.
const DefaultMode = Daemon

// Config aggregates role-specific configuration (file key role.*).
type Config struct {
	// Mode selects the runtime role (file key role.mode): daemon / gateway / cli.
	// It replaces the former role-selecting subcommand, so the single `tango`
	// binary dispatches purely on this key (file / TANGO_ROLE_MODE / --role.mode).
	Mode string `mapstructure:"mode"`
	// Daemon is the daemon role config (file key role.daemon.*).
	Daemon *daemon.Config `mapstructure:"daemon"`
	// Gateway is the HTTP gateway role config (file key role.gateway.*).
	Gateway *gateway.Config `mapstructure:"gateway"`
}

// Validate delegates to the configured role sub-configs.
func (c *Config) Validate() error {
	switch c.Mode {
	case Daemon, Gateway, CLI:
	default:
		return fmt.Errorf("mode %q is invalid (want one of %q / %q / %q)",
			c.Mode, Daemon, Gateway, CLI)
	}
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
	set(prefix+".mode", "")
	new(daemon.Config).RegisterDefaults(set, prefix+".daemon")
	new(gateway.Config).RegisterDefaults(set, prefix+".gateway")
}

// ApplyDefaults allocates child configs and lets them own their defaults.
func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = DefaultMode
	}
	if c.Daemon == nil {
		c.Daemon = &daemon.Config{}
	}
	c.Daemon.ApplyDefaults()
	if c.Gateway == nil {
		c.Gateway = &gateway.Config{}
	}
	c.Gateway.ApplyDefaults()
}
