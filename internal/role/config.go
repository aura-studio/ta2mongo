package role

// Config configures runtime role behavior.
type Config struct {
	// Mode selects the runtime role. Only report is supported.
	Mode string `mapstructure:"mode"`
}
