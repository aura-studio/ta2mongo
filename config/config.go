package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

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
	// LogPattern is a list of regex patterns matched against file paths.
	LogPattern []string `mapstructure:"logPattern"`
}

// TailConfig controls the file-tailing behavior.
type TailConfig struct {
	RescanSeconds int `mapstructure:"rescanSeconds"`
}

// BatchConfig controls batching and parallelism of writes.
type BatchConfig struct {
	SizeMin int `mapstructure:"sizeMin"`
	// SizeInitial is the effective batch size at "mid backlog".
	SizeInitial int `mapstructure:"sizeInitial"`
	SizeMax     int `mapstructure:"sizeMax"`

	WorkerCount     int `mapstructure:"workerCount"`
	WorkerChSize    int `mapstructure:"workerChSize"`
	FlushIntervalMs int `mapstructure:"flushIntervalMs"`
}

// RetryConfig controls the exponential-backoff retry for bulk writes.
type RetryConfig struct {
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`
}

// LogConfig controls logging output.
type LogConfig struct {
	Level string `mapstructure:"level"`
}

// Load reads the YAML config file specified by path, applies defaults,
// validates the result and returns the final Config.
func Load(path string) (Config, error) {
	v := viper.New()
	v.SetConfigFile(path)

	// Set defaults so unspecified fields get reasonable values.
	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// setDefaults registers only the mode default via viper. All other field
// defaults are applied in validate() to keep a single source of truth and
// avoid maintaining the same default value in two places.
func setDefaults(v *viper.Viper) {
	v.SetDefault("mode", ModeDaemon)
}

func (c *Config) validate() error {
	switch c.Mode {
	case ModeDaemon, ModeOnce, ModeIngest:
		// valid
	case "":
		c.Mode = ModeDaemon
	default:
		return fmt.Errorf("config: mode must be one of %q, %q, %q; got %q", ModeDaemon, ModeOnce, ModeIngest, c.Mode)
	}

	if c.Mongo.URI == "" {
		return fmt.Errorf("config: mongo.uri is required")
	}

	if c.Tail.RescanSeconds <= 0 {
		c.Tail.RescanSeconds = 30
	}

	// Ensure dynamic batch sizes are sane.
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

	// Clamp ordering into a valid range: sizeMin <= sizeInitial <= sizeMax.
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

	if c.Batch.WorkerCount <= 0 {
		c.Batch.WorkerCount = 2
	}
	if c.Batch.WorkerChSize <= 0 {
		c.Batch.WorkerChSize = 1000
	}
	if c.Batch.FlushIntervalMs <= 0 {
		c.Batch.FlushIntervalMs = 1000
	}

	if c.Retry.MaxElapsedTime <= 0 {
		c.Retry.MaxElapsedTime = 10 * time.Second
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}

	return nil
}

// MongoDBFromURI extracts the database name from a MongoDB URI path.
// Example:
// - mongodb://host:27017/ta2mongo => ta2mongo
// - mongodb://host:27017           => error
func MongoDBFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse mongo uri: %w", err)
	}

	// AWS DocumentDB / some MongoDB connection strings may not include a
	// database name in the URI path (e.g. .../?:tls=true...).
	// In that case we fall back to the project's default database name.
	db := strings.Trim(u.Path, "/")
	if db == "" {
		return "ta2mongo", nil
	}
	return db, nil
}

// FlushInterval returns Batch.FlushIntervalMs as a time.Duration.
func (c *Config) FlushInterval() time.Duration {
	return time.Duration(c.Batch.FlushIntervalMs) * time.Millisecond
}

// RescanInterval returns Tail.RescanSeconds as a time.Duration.
func (c *Config) RescanInterval() time.Duration {
	return time.Duration(c.Tail.RescanSeconds) * time.Second
}
