package uploadfile

// Config configures the one-shot file-import source (file key
// source.uploadfile.*). It is the uploadfile module's own configuration; the
// source package's aggregate Config references it by pointer and the loader
// unmarshals the source.uploadfile.* keys into it.
type Config struct {
	// LogPattern is a list of glob patterns (the tailer's pattern syntax,
	// including ** and cross-platform paths) matched against file paths.
	// Required by the faces that run the import (cli function=uploadfile,
	// Engine.UploadFile); an empty list is a valid config that simply has
	// nothing to import yet.
	LogPattern []string `mapstructure:"logPattern"`
	// MaxLineBytes caps a single log line's length. Default 10485760 (10 MB),
	// matching the tailer.
	MaxLineBytes int `mapstructure:"maxLineBytes"`
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding works.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".logPattern", []string{})
	set(prefix+".maxLineBytes", 0)
}

// ApplyDefaults fills unset uploadfile options.
func (c *Config) ApplyDefaults() {
	if c.MaxLineBytes <= 0 {
		c.MaxLineBytes = defaultMaxLineSize
	}
}
