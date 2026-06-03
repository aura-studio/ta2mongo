package role

const (
	// ModeDaemon is the daemon service runtime: tail logs -> filter -> Mongo.
	ModeDaemon = "daemon"
)

// Config configures runtime role behavior.
type Config struct {
	// Mode selects the runtime role. Only daemon is supported here.
	Mode string `mapstructure:"mode"`
}

// ApplyDefaults fills unset role options.
func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = ModeDaemon
	}
}
