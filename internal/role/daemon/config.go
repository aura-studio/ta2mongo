package daemon

// Config is the daemon role's own configuration (file key role.daemon). The
// daemon is driven entirely by the shared top-level modules (logging, dao,
// parser, source, process), so it currently has no role-specific knobs — the
// section exists for schema symmetry with the other roles.
type Config struct{}

// ApplyDefaults is a no-op (no fields yet).
func (c *Config) ApplyDefaults() {}

// Validate is a no-op (no fields yet).
func (c *Config) Validate() error { return nil }

// RegisterDefaults registers no keys (no fields yet); kept for cascade symmetry.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {}
