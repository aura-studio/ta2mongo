// Package config defines the tango configuration structure and loading logic.
//
// Configuration is loaded from three sources in increasing priority order:
//  1. Built-in defaults
//  2. YAML config file (optional; skipped if the file does not exist)
//  3. Environment variables (prefix: TANGO_, e.g. TANGO_MONGO_URI)
//  4. CLI flags (highest priority; set by the caller via ApplyFlags)
//
// All YAML keys use camelCase. Environment variable names are derived by
// upper-casing the key and replacing dots/underscores with underscores, e.g.
//
//	mongo.uri  => TANGO_MONGO_URI
//	log.level  => TANGO_LOG_LEVEL
//	batch.sizeMin => TANGO_BATCH_SIZEMIN
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

// Config is the top-level configuration, mapping 1-to-1 with the YAML structure.
type Config struct {
	Mode  string      `mapstructure:"mode"`
	Mongo MongoConfig `mapstructure:"mongo"`
	TA    TAConfig    `mapstructure:"ta"`
	Tail  TailConfig  `mapstructure:"tail"`
	Batch BatchConfig `mapstructure:"batch"`
	Retry RetryConfig `mapstructure:"retry"`
	Log   LogConfig   `mapstructure:"log"`
}

// MongoConfig holds MongoDB connection parameters.
type MongoConfig struct {
	URI string `mapstructure:"uri"`
}

// TAConfig holds ThinkingData log source settings.
type TAConfig struct {
	// LogPattern is a list of glob/regex patterns matched against file paths.
	LogPattern []string `mapstructure:"logPattern"`
}

// TailConfig controls the file-tailing behavior.
type TailConfig struct {
	// RescanSeconds is how often (in seconds) the tailer rescans for new files.
	RescanSeconds int `mapstructure:"rescanSeconds"`
}

// BatchConfig controls batching and parallelism of writes.
type BatchConfig struct {
	// SizeMin is the minimum batch size (adaptive lower bound).
	SizeMin int `mapstructure:"sizeMin"`
	// SizeInitial is the effective batch size at "mid backlog".
	SizeInitial int `mapstructure:"sizeInitial"`
	// SizeMax is the maximum batch size (adaptive upper bound).
	SizeMax int `mapstructure:"sizeMax"`

	// Workers is the number of parallel write workers.
	Workers int `mapstructure:"workers"`
	// ChannelSize is the per-worker channel buffer size.
	ChannelSize int `mapstructure:"channelSize"`
	// FlushInterval is how often workers flush partial batches (e.g. "1s").
	FlushInterval time.Duration `mapstructure:"flushInterval"`
}

// RetryConfig controls the exponential-backoff retry for bulk writes.
type RetryConfig struct {
	// MaxElapsedTime is the maximum total time spent retrying a single bulk write.
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`
}

// LogConfig controls logging output.
type LogConfig struct {
	Level string `mapstructure:"level"`
}

// Load builds a Config from:
//  1. Defaults
//  2. YAML file at path (skipped silently if the file does not exist)
//  3. Environment variables (TANGO_ prefix)
//  4. CLI flags bound via BindFlags (caller must call BindFlags before Load)
//
// If path is empty, file loading is skipped entirely.
// The config is validated before being returned.
func Load(path string, flags *pflag.FlagSet) (Config, error) {
	v := viper.New()

	// Environment variables: TANGO_MONGO_URI, TANGO_LOG_LEVEL, etc.
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
		// If ErrNotExist: silently skip; use defaults + env + flags.
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
// Only flags that were explicitly set on the CLI take effect; unset flags
// fall back to the file / env / default chain.
func bindFlags(v *viper.Viper, flags *pflag.FlagSet) error {
	var bindErr error
	flags.VisitAll(func(f *pflag.Flag) {
		if bindErr != nil {
			return
		}
		// Map flag name (e.g. "mongo.uri") to viper key.
		key := f.Name
		if err := v.BindPFlag(key, f); err != nil {
			bindErr = fmt.Errorf("bind flag %q: %w", key, err)
		}
	})
	return bindErr
}

// setDefaults registers viper defaults for all fields.
func setDefaults(v *viper.Viper) {
	v.SetDefault("mode", ModeDaemon)
	v.SetDefault("mongo.uri", "")
	v.SetDefault("ta.logPattern", []string{})
	v.SetDefault("tail.rescanSeconds", 30)
	v.SetDefault("batch.sizeMin", 0)     // 0 = auto (1/4 of sizeInitial)
	v.SetDefault("batch.sizeInitial", 1000)
	v.SetDefault("batch.sizeMax", 0)     // 0 = auto (2x sizeInitial)
	v.SetDefault("batch.workers", 2)
	v.SetDefault("batch.channelSize", 1000)
	v.SetDefault("batch.flushInterval", "1s")
	v.SetDefault("retry.maxElapsedTime", "10s")
	v.SetDefault("log.level", "info")
}

// applyDefaults fills in zero-value fields with sensible derived defaults
// and clamps values into valid ranges.
func applyDefaults(c *Config) {
	if c.Mode == "" {
		c.Mode = ModeDaemon
	}

	if c.Tail.RescanSeconds <= 0 {
		c.Tail.RescanSeconds = 30
	}

	if c.Batch.SizeInitial <= 0 {
		c.Batch.SizeInitial = 1000
	}
	if c.Batch.SizeMin <= 0 {
		c.Batch.SizeMin = c.Batch.SizeInitial / 4
		if c.Batch.SizeMin < 1 {
			c.Batch.SizeMin = 1
		}
	}
	if c.Batch.SizeMax <= 0 {
		c.Batch.SizeMax = c.Batch.SizeInitial * 2
	}

	// Clamp: sizeMin <= sizeInitial <= sizeMax.
	if c.Batch.SizeMin > c.Batch.SizeInitial {
		c.Batch.SizeMin = c.Batch.SizeInitial
	}
	if c.Batch.SizeMax < c.Batch.SizeInitial {
		c.Batch.SizeMax = c.Batch.SizeInitial
	}
	if c.Batch.SizeMax < c.Batch.SizeMin {
		c.Batch.SizeMax = c.Batch.SizeMin
	}
	if c.Batch.SizeMin < 1 {
		c.Batch.SizeMin = 1
	}
	if c.Batch.SizeInitial < 1 {
		c.Batch.SizeInitial = 1
	}
	if c.Batch.SizeMax < 1 {
		c.Batch.SizeMax = 1
	}

	if c.Batch.Workers <= 0 {
		c.Batch.Workers = 2
	}
	if c.Batch.ChannelSize <= 0 {
		c.Batch.ChannelSize = 1000
	}
	if c.Batch.FlushInterval <= 0 {
		c.Batch.FlushInterval = time.Second
	}

	if c.Retry.MaxElapsedTime <= 0 {
		c.Retry.MaxElapsedTime = 10 * time.Second
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
}

// Validate checks that required fields are present.
// It is called by the cmd layer after flags are merged in.
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeDaemon, ModeOnce, ModeIngest:
		// valid
	default:
		return fmt.Errorf("config: mode must be one of %q, %q, %q; got %q",
			ModeDaemon, ModeOnce, ModeIngest, c.Mode)
	}
	if c.Mongo.URI == "" {
		return fmt.Errorf("config: mongo.uri is required (set via --mongo.uri, TANGO_MONGO_URI, or config file)")
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

	// AWS DocumentDB / some MongoDB connection strings may not include a
	// database name in the URI path (e.g. .../?tls=true...).
	// In that case fall back to the project's default database name.
	db := strings.Trim(u.Path, "/")
	if db == "" {
		return "tango", nil
	}
	return db, nil
}

// FlushInterval returns Batch.FlushInterval (already a time.Duration).
func (c *Config) FlushInterval() time.Duration {
	return c.Batch.FlushInterval
}

// RescanInterval returns Tail.RescanSeconds as a time.Duration.
func (c *Config) RescanInterval() time.Duration {
	return time.Duration(c.Tail.RescanSeconds) * time.Second
}
