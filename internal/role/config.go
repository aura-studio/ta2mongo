package role

const (
	// ModeReport is the report service runtime: tail logs -> filter -> Mongo.
	ModeReport = "report"
)

// Config configures runtime role behavior.
type Config struct {
	// Mode selects the runtime role. Only report is supported.
	Mode string `mapstructure:"mode"`
}

// ApplyDefaults fills unset role options.
func (c *Config) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = ModeReport
	}
}
