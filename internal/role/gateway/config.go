// Package gateway implements the HTTP gateway role: an HTTP server that wraps
// each request body as an httpbody source and ingests it through the embedded
// api engine. Its file config lives under the package-path key role.gateway and
// holds only gateway-specific knobs — the processing config (mode, batch size,
// pipeline) and the reporting filter are the shared top-level process.* /
// parser.filter.* modules, not duplicated here.
package gateway

// DefaultServerAddr is the default HTTP listen address for `tango gateway`.
const DefaultServerAddr = ":8080"

// Config is the gateway role's own configuration (file key role.gateway). The
// shared settings (MongoDB, logging, processing, filter) live at the top level
// (dao.*, logging.*, process.*, parser.filter.*) and are injected by the
// command; only the genuinely gateway-specific knobs live here.
type Config struct {
	// Addr is the HTTP listen address (file key role.gateway.addr).
	Addr string `mapstructure:"addr"`
}

// ApplyDefaults fills unset options with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.Addr == "" {
		c.Addr = DefaultServerAddr
	}
}

// Validate checks gateway-specific config.
func (c *Config) Validate() error {
	return nil
}

// RegisterDefaults registers this role's config keys (under prefix).
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".addr", "")
}
