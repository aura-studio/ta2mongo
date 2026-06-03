package log

// Config configures log output. It is the log module's own configuration; the
// top-level config package references it by pointer and the loader unmarshals
// the logging.* keys into it.
type Config struct {
	// Level is the log verbosity: debug, info, warn, error. Default "info".
	Level string `mapstructure:"level"`
	// Format selects the log encoding: "text" (default) or "json".
	Format string `mapstructure:"format"`
}
