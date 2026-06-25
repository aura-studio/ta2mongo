package file

// Config configures the one-shot file-import source (config key source.file.*).
// It is the file module's own configuration; the source package's aggregate
// Config references it by pointer and the loader unmarshals the source.file.*
// keys into it.
type Config struct {
	// Paths is the list of explicit file paths to import. They are taken
	// verbatim — no glob expansion and no directory walking (a directory path is
	// skipped, not expanded). Required by the faces that run the import (cli
	// function=file, Engine.File); an empty list is a valid config that simply
	// has nothing to import yet.
	Paths []string `mapstructure:"paths"`
	// MaxLineBytes caps a single log line's length. Default 10485760 (10 MB).
	MaxLineBytes int `mapstructure:"maxLineBytes"`
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding works.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".paths", []string{})
	set(prefix+".maxLineBytes", 0)
}

// ApplyDefaults fills unset file options.
func (c *Config) ApplyDefaults() {
	if c.MaxLineBytes <= 0 {
		c.MaxLineBytes = defaultMaxLineSize
	}
}
