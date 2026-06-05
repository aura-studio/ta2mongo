package cli

import "fmt"

// Function selectors for the cli role (file key role.cli.function).
const (
	// FunctionUpload is the default: read TA log lines from stdin and ingest them
	// with the configured process.mode (the console equivalent of /upload).
	FunctionUpload = "upload"
	// FunctionData reads a single Extended-JSON Mongo Data API request from stdin,
	// executes it, and writes the Extended-JSON response to stdout (the console
	// equivalent of the gateway POST /data endpoint).
	FunctionData = "data"
)

// DefaultFunction is the cli function used when role.cli.function is unset.
const DefaultFunction = FunctionUpload

// Config is the cli role's own configuration (file key role.cli). The shared
// settings (MongoDB, processing, filter) live at the top level; only the
// cli-specific knobs live here.
type Config struct {
	// Function selects what the cli role does (file key role.cli.function):
	// upload (stdin log ingest) or data (stdin Mongo Data API request).
	Function string `mapstructure:"function"`
}

// ApplyDefaults fills unset options with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.Function == "" {
		c.Function = DefaultFunction
	}
}

// Validate checks cli-specific config.
func (c *Config) Validate() error {
	switch c.Function {
	case FunctionUpload, FunctionData:
		return nil
	default:
		return fmt.Errorf("function %q is invalid (want %q / %q)",
			c.Function, FunctionUpload, FunctionData)
	}
}

// RegisterDefaults registers this role's config keys (under prefix).
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".function", "")
}
