// Package config defines the tango configuration structure and loading logic.
//
// Configuration is loaded from four sources in increasing priority order:
//  1. Built-in defaults
//  2. YAML config file (optional; skipped silently if the file does not exist)
//  3. Environment variables (prefix: TANGO_, e.g. TANGO_MONGOURI)
//  4. CLI flags (highest priority)
//
// All YAML keys and CLI flag names are flat camelCase, e.g.
//
//	mongoURI        => TANGO_MONGOURI         / --mongoURI
//	logLevel        => TANGO_LOGLEVEL         / --logLevel
//	batchSize       => TANGO_BATCHSIZE        / --batchSize
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Mode constants for the run mode configuration.
const (
	ModeDaemon = "daemon"
	ModeOnce   = "once"
	ModeIngest = "ingest"
)

// Config is the flat top-level configuration.
// All fields map directly to YAML keys, CLI flags, and TANGO_* env vars.
type Config struct {
	// Mode selects the run mode: daemon (default), once, or ingest.
	Mode string `mapstructure:"mode"`

	// MongoURI is the MongoDB connection URI (required).
	// The database name is extracted from the URI path.
	MongoURI string `mapstructure:"mongoURI"`

	// LogPattern is a list of regex patterns matched against file paths.
	// Required for daemon and once modes; ignored by ingest.
	LogPattern []string `mapstructure:"logPattern"`

	// RescanInterval is how often the tailer rescans for new files (daemon only).
	RescanInterval time.Duration `mapstructure:"rescanInterval"`

	// BatchSize is the target number of records per bulk-write flush.
	// The adaptive min is BatchSize/4 and max is BatchSize*2.
	BatchSize int `mapstructure:"batchSize"`

	// BatchWorkers is the number of parallel write workers.
	BatchWorkers int `mapstructure:"batchWorkers"`

	// FlushInterval is how often workers flush partial batches (e.g. "1s").
	FlushInterval time.Duration `mapstructure:"flushInterval"`

	// MaxElapsedTime is the maximum total retry time for a single bulk write.
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`

	// LogLevel is the log verbosity: debug, info, warn, error.
	LogLevel string `mapstructure:"logLevel"`
}

// BatchSizeMin returns the adaptive lower bound (BatchSize / 4, minimum 1).
func (c Config) BatchSizeMin() int {
	v := c.BatchSize / 4
	if v < 1 {
		return 1
	}
	return v
}

// BatchSizeMax returns the adaptive upper bound (BatchSize * 2).
func (c Config) BatchSizeMax() int {
	return c.BatchSize * 2
}

// BatchChannelSize returns the per-worker channel buffer size (BatchSize * 2).
func (c Config) BatchChannelSize() int {
	return c.BatchSize * 2
}


// Load builds a Config from defaults → YAML file → env vars → CLI flags.
//
// If path is empty or the file does not exist, file loading is skipped silently.
func Load(path string, flags *pflag.FlagSet) (Config, error) {
	v := viper.New()

	// Environment variables: TANGO_MONGOURI, TANGO_LOGLEVEL, etc.
	v.SetEnvPrefix("TANGO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Register defaults.
	setDefaults(v)

	// Load YAML file (optional).
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			v.SetConfigFile(path)
			if err := v.ReadInConfig(); err != nil {
				return Config{}, fmt.Errorf("read config %q: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("stat config %q: %w", path, err)
		}
		// ErrNotExist: silently skip; use defaults + env + flags.
	}

	// Bind CLI flags (flags override env vars and file).
	if flags != nil {
		if err := bindFlags(v, flags); err != nil {
			return Config{}, err
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	applyDefaults(&cfg)
	return cfg, nil
}

// bindFlags binds every flag in the set to its matching viper key.
// Only flags explicitly set on the CLI take effect; unset flags
// fall back to the file / env / default chain.
func bindFlags(v *viper.Viper, flags *pflag.FlagSet) error {
	var bindErr error
	flags.VisitAll(func(f *pflag.Flag) {
		if bindErr != nil {
			return
		}
		if err := v.BindPFlag(f.Name, f); err != nil {
			bindErr = fmt.Errorf("bind flag %q: %w", f.Name, err)
		}
	})
	return bindErr
}

// setDefaults registers viper defaults for all fields.
func setDefaults(v *viper.Viper) {
	v.SetDefault("mode", ModeDaemon)
	v.SetDefault("mongoURI", "")
	v.SetDefault("logPattern", []string{})
	v.SetDefault("rescanInterval", "30s")
	v.SetDefault("batchSize", 1000)
	v.SetDefault("batchWorkers", 2)
	v.SetDefault("flushInterval", "1s")
	v.SetDefault("maxElapsedTime", "10s")
	v.SetDefault("logLevel", "info")
}

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(c *Config) {
	if c.Mode == "" {
		c.Mode = ModeDaemon
	}
	if c.RescanInterval <= 0 {
		c.RescanInterval = 30 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.BatchWorkers <= 0 {
		c.BatchWorkers = 2
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = time.Second
	}
	if c.MaxElapsedTime <= 0 {
		c.MaxElapsedTime = 10 * time.Second
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
}

// Validate checks that required fields are present.
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeDaemon, ModeOnce, ModeIngest:
		// valid
	default:
		return fmt.Errorf("config: mode must be one of %q, %q, %q; got %q",
			ModeDaemon, ModeOnce, ModeIngest, c.Mode)
	}
	if c.MongoURI == "" {
		return fmt.Errorf("config: mongoURI is required (set via --mongoURI, TANGO_MONGOURI, or config file)")
	}
	return nil
}

// MongoDBFromURI extracts the database name from a MongoDB URI path.
// Examples:
//   - mongodb://host:27017/tango => "tango"
//   - mongodb://host:27017       => "tango" (default fallback)
func MongoDBFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse mongo uri: %w", err)
	}
	db := strings.Trim(u.Path, "/")
	if db == "" {
		return "tango", nil
	}
	return db, nil
}
