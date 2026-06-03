package engine

// Config configures runtime engine behavior.
type Config struct {
	// Mode selects the runtime role. Only report is supported.
	Mode string `mapstructure:"mode"`
}
