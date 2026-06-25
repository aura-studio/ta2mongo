package cli

import "fmt"

// Function selectors for the cli role (file key role.cli.function).
const (
	// FunctionUpload is the default: read TA log lines from stdin and ingest them
	// with the configured process.mode (the console equivalent of /upload).
	FunctionUpload = "upload"
	// FunctionFile bulk imports the explicitly-listed on-disk log files
	// (source.file.paths) once (read to EOF, no checkpoint) and ingests them with
	// the configured process.mode. Explicit paths only — no glob, no directories.
	// The finite counterpart of the daemon's tailer; re-running re-imports
	// everything (events and user_set-style ops converge; accumulating
	// user_add/user_append re-apply).
	FunctionFile = "file"
	// FunctionEJSON reads a single Extended-JSON Mongo Data API request from stdin,
	// executes it, and writes the Extended-JSON response to stdout (the console
	// equivalent of the gateway POST /ejson endpoint).
	FunctionEJSON = "ejson"
	// FunctionSQL reads a single SQL statement from stdin, executes it via the SQL
	// Data API, and writes the Extended-JSON result to stdout (the console
	// equivalent of the gateway POST /sql endpoint).
	FunctionSQL = "sql"
	// FunctionConfig reads a single runtime config document (JSON) from stdin,
	// publishes it to the central cfgsync collection, and writes {"version":<new>}
	// to stdout (the console equivalent of the gateway POST /config endpoint).
	// role.cli.configMode selects set (default, replace) or append (merge).
	FunctionConfig = "config"
	// FunctionConfigGet fetches the current central config document and writes it
	// as JSON to stdout (the console equivalent of the gateway GET /config
	// endpoint); exits with an error when nothing has been published yet.
	FunctionConfigGet = "configget"
)

// DefaultFunction is the cli function used when role.cli.function is unset.
const DefaultFunction = FunctionUpload

// Config is the cli role's own configuration (file key role.cli). The shared
// settings (MongoDB, processing, filter) live at the top level; only the
// cli-specific knobs live here.
type Config struct {
	// Function selects what the cli role does (file key role.cli.function):
	// upload (stdin log ingest), file (one-shot import of the explicit files in
	// source.file.paths), ejson (stdin Mongo Data API request), sql (stdin SQL
	// statement), config (stdin config publish), or configget (fetch the central
	// config document).
	Function string `mapstructure:"function"`
	// ConfigMode selects the publish mode for function=config (file key
	// role.cli.configMode): set (default, replace the stored subtrees) or
	// append (union the published filter rules into the stored ones).
	ConfigMode string `mapstructure:"configMode"`
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
	case FunctionUpload, FunctionFile, FunctionEJSON, FunctionSQL, FunctionConfig, FunctionConfigGet:
	default:
		return fmt.Errorf("function %q is invalid (want %q / %q / %q / %q / %q / %q)",
			c.Function, FunctionUpload, FunctionFile, FunctionEJSON, FunctionSQL, FunctionConfig, FunctionConfigGet)
	}
	switch c.ConfigMode {
	case "", "set", "append":
		return nil
	default:
		return fmt.Errorf("configMode %q is invalid (want set or append)", c.ConfigMode)
	}
}

// RegisterDefaults registers this role's config keys (under prefix).
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".function", "")
	set(prefix+".configMode", "")
}
