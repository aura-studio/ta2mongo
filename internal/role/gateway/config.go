// Package gateway implements the HTTP gateway role: an HTTP server that wraps
// each request body as an httpbody source and ingests it through the embedded
// api engine (single / batch / pipeline). Its file config lives under the
// package-path key role.gateway and holds only gateway-specific knobs — the
// processing config (batch size, pipeline) and the reporting filter are the
// shared top-level process.* / parser.filter.* modules, not duplicated here.
package gateway

import (
	"rocket-nano/tools/tango/internal/process"
)

// DefaultServerAddr is the default HTTP listen address for `tango gateway`.
const DefaultServerAddr = ":8080"

// Config is the gateway role's own configuration (file key role.gateway). The
// shared settings (MongoDB, logging, processing, filter) live at the top level
// (dao.*, logging.*, process.*, parser.filter.*) and are injected by the
// command; only the genuinely gateway-specific knobs live here.
type Config struct {
	// Addr is the HTTP listen address (file key role.gateway.addr).
	Addr string `mapstructure:"addr"`
	// DefaultMode selects the strategy when a request omits "mode":
	// single | batch | pipeline. Default batch.
	DefaultMode string `mapstructure:"defaultMode"`
}

// ApplyDefaults fills unset options with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.Addr == "" {
		c.Addr = DefaultServerAddr
	}
	if c.DefaultMode == "" {
		c.DefaultMode = string(process.ModeBatch)
	}
}

// Validate checks the default upload mode is a valid strategy.
func (c *Config) Validate() error {
	if c.DefaultMode != "" {
		if _, err := process.ParseMode(c.DefaultMode); err != nil {
			return err
		}
	}
	return nil
}

// RegisterDefaults registers this role's config keys (under prefix).
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".addr", "")
	set(prefix+".defaultMode", "")
}
