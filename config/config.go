// Package config defines tango's configuration.
//
// The file-facing schema is DaemonConfig (daemon.go, for the standalone/cluster
// daemon config files); it projects onto the shared runtime Config in this
// file, which the internal packages (daemon/pipeline/store/remoteconfig)
// consume. loader.go holds the shared YAML/JSON + TANGO_* env + flag helpers.
//
// Sources, in increasing priority: built-in defaults < config file (YAML or
// JSON, by extension) < TANGO_* environment variables < CLI flags. In cluster
// mode the remote-config document (MongoDB) additionally overrides the report
// filter and is applied separately at startup / on the sync interval.
//
// The runtime Load below loads the flat runtime Config directly; it is retained
// for tests and remote-config merging — the daemon binary uses LoadDaemon.
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

	"rocket-nano/tools/tango/internal/core/filter"
)

// ModeDaemon is the only run mode: the reporting daemon (standalone / cluster
// are daemon sub-modes selected by subcommand, not values of this field).
const ModeDaemon = "daemon"

// Remote-config defaults (the cluster-mode control-plane document).
const (
	DefaultRemoteConfigCollection = "_tango_config"
	DefaultRemoteConfigDocumentID = "default"
	DefaultRemoteConfigInterval   = time.Hour
)

// TailMode constants control how the tailer watches for file changes.
const (
	// TailModeHybrid uses an event-driven watcher as the primary mechanism with
	// a periodic poll fallback. Combines low latency with reliability.
	TailModeHybrid = "hybrid"
	// TailModePoll uses a simple polling loop (read → sleep → retry).
	TailModePoll = "poll"
	// TailModeEvent uses the kqueue/inotify event-driven watcher only.
	TailModeEvent = "event"
)

// Config is the top-level runtime configuration consumed by the internal
// packages. YAML keys are the mapstructure tags; env vars use the TANGO_ prefix
// with "." → "_" (e.g. mongo.uri → TANGO_MONGO_URI).
type Config struct {
	// Mode is always ModeDaemon at runtime.
	Mode string `mapstructure:"mode"`

	// Logging configures log output.
	Logging LoggingConfig `mapstructure:"logging"`

	// Mongo configures the MongoDB connection and write-retry behaviour.
	Mongo MongoConfig `mapstructure:"mongo"`

	// Source configures the file-tailing data source.
	Source SourceConfig `mapstructure:"source"`

	// Pipeline configures batching and parallel write workers.
	Pipeline PipelineConfig `mapstructure:"pipeline"`

	// Filter configures the reporting (upload) filter: expr-lang include/exclude
	// rules applied to every tailed record.
	Filter FilterConfig `mapstructure:"filter"`

	// RemoteConfig enables the cluster-mode control-plane override: a single
	// JSON document in MongoDB whose filter is merged on top of this config at
	// startup and re-fetched every SyncInterval (hot-reloaded). Connection
	// fields (mongo.uri, remoteConfig itself) are never overridable remotely.
	RemoteConfig RemoteConfig `mapstructure:"remoteConfig"`
}

// LoggingConfig configures log output.
type LoggingConfig struct {
	// Level is the log verbosity: debug, info, warn, error. Default "info".
	Level string `mapstructure:"level"`
	// Format selects the log encoding: "text" (default) or "json".
	Format string `mapstructure:"format"`
}

// MongoConfig configures the MongoDB connection and write-retry behaviour.
type MongoConfig struct {
	// URI is the MongoDB connection URI (required). The database name is taken
	// from the URI path.
	URI string `mapstructure:"uri"`
	// MaxElapsedTime is the maximum total retry time for a single bulk write.
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`
	// ConnectTimeout bounds the initial connection handshake.
	ConnectTimeout time.Duration `mapstructure:"connectTimeout"`
	// ServerSelectionTimeout bounds how long the driver waits for a suitable
	// server before failing an operation.
	ServerSelectionTimeout time.Duration `mapstructure:"serverSelectionTimeout"`
}

// SourceConfig configures the file-tailing data source.
type SourceConfig struct {
	// LogPattern is a list of glob/regex patterns matched against file paths.
	// Required.
	LogPattern []string `mapstructure:"logPattern"`
	// TailMode selects the file-tailing strategy: hybrid (default) / poll / event.
	TailMode string `mapstructure:"tailMode"`
	// RescanInterval is how often the tailer rescans for new files.
	RescanInterval time.Duration `mapstructure:"rescanInterval"`
	// PollInterval is the poll cadence used by poll / hybrid tail modes.
	PollInterval time.Duration `mapstructure:"pollInterval"`
	// MaxLineBytes caps a single log line's length.
	MaxLineBytes int `mapstructure:"maxLineBytes"`
}

// PipelineConfig configures batching and parallel write workers.
type PipelineConfig struct {
	// BatchSize is the target number of records per bulk-write flush.
	BatchSize int `mapstructure:"batchSize"`
	// BatchSizeMin is the adaptive lower bound (0 ⇒ auto-derive BatchSize/4).
	BatchSizeMin int `mapstructure:"batchSizeMin"`
	// BatchSizeMax is the adaptive upper bound (0 ⇒ auto-derive BatchSize*2).
	BatchSizeMax int `mapstructure:"batchSizeMax"`
	// BatchWorkers is the number of parallel write workers.
	BatchWorkers int `mapstructure:"batchWorkers"`
	// FlushInterval is how often workers flush partial batches.
	FlushInterval time.Duration `mapstructure:"flushInterval"`
	// ChannelBuffer is the per-worker line-channel buffer (0 ⇒ BatchSize*2).
	ChannelBuffer int `mapstructure:"channelBuffer"`
	// DeadLetterCap is the per-worker dead-letter batch capacity.
	DeadLetterCap int `mapstructure:"deadLetterCap"`
}

// FilterConfig configures the expr-lang include/exclude rules.
type FilterConfig struct {
	// Include is a list of expr-lang expressions. If non-empty, a record is kept
	// only when at least one expression evaluates to true (OR semantics).
	Include []string `mapstructure:"include"`
	// Exclude is a list of expr-lang expressions. A record is dropped if any
	// expression evaluates to true. Applied after Include.
	Exclude []string `mapstructure:"exclude"`
}

// RemoteConfig controls the MongoDB-backed configuration override (cluster mode).
type RemoteConfig struct {
	// Enabled turns the remote override on. Set by the daemon's cluster mode.
	Enabled bool `mapstructure:"enabled"`
	// Collection is the MongoDB collection holding the config document.
	Collection string `mapstructure:"collection"`
	// DocumentID is the _id of the single shared config document.
	DocumentID string `mapstructure:"documentID"`
	// SyncInterval is how often cluster mode re-fetches the document.
	SyncInterval time.Duration `mapstructure:"syncInterval"`
}

// BatchSizeMin returns the adaptive lower bound for batch sizing.
func (c Config) BatchSizeMin() int {
	if c.Pipeline.BatchSizeMin > 0 {
		if c.Pipeline.BatchSizeMin > c.Pipeline.BatchSize {
			return c.Pipeline.BatchSize
		}
		return c.Pipeline.BatchSizeMin
	}
	v := c.Pipeline.BatchSize / 4
	if v < 1 {
		return 1
	}
	return v
}

// BatchSizeMax returns the adaptive upper bound for batch sizing.
func (c Config) BatchSizeMax() int {
	if c.Pipeline.BatchSizeMax > 0 {
		if c.Pipeline.BatchSizeMax < c.Pipeline.BatchSize {
			return c.Pipeline.BatchSize
		}
		return c.Pipeline.BatchSizeMax
	}
	return c.Pipeline.BatchSize * 2
}

// BatchChannelSize returns the per-worker channel buffer size.
func (c Config) BatchChannelSize() int {
	if c.Pipeline.ChannelBuffer > 0 {
		return c.Pipeline.ChannelBuffer
	}
	return c.Pipeline.BatchSize * 2
}

// Load builds a Config from defaults → YAML/JSON file → TANGO_* env → flags.
// Retained for tests and remote-config merging; the daemon binary uses
// LoadDaemon.
func Load(path string, flags *pflag.FlagSet) (Config, error) {
	v := viper.New()
	setDefaults(v)

	if path != "" {
		if _, err := os.Stat(path); err == nil {
			v.SetConfigFile(path)
			if err := v.ReadInConfig(); err != nil {
				return Config{}, fmt.Errorf("read config %q: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("stat config %q: %w", path, err)
		}
	}

	applyEnvOverrides(v)

	if flags != nil {
		flags.Visit(func(f *pflag.Flag) { _ = v.BindPFlag(f.Name, f) })
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}
	applyDefaults(&cfg)
	return cfg, nil
}

// applyEnvOverrides reads TANGO_* environment variables and v.Set()s them so
// they override values from YAML or defaults (CLI flags, bound after, win).
func applyEnvOverrides(v *viper.Viper) {
	intKeys := map[string]string{
		"TANGO_SOURCE_MAXLINEBYTES":      "source.maxLineBytes",
		"TANGO_PIPELINE_BATCH_SIZE":      "pipeline.batchSize",
		"TANGO_PIPELINE_BATCH_SIZE_MIN":  "pipeline.batchSizeMin",
		"TANGO_PIPELINE_BATCH_SIZE_MAX":  "pipeline.batchSizeMax",
		"TANGO_PIPELINE_BATCH_WORKERS":   "pipeline.batchWorkers",
		"TANGO_PIPELINE_CHANNEL_BUFFER":  "pipeline.channelBuffer",
		"TANGO_PIPELINE_DEAD_LETTER_CAP": "pipeline.deadLetterCap",
	}
	for envKey, cfgKey := range intKeys {
		if val := os.Getenv(envKey); val != "" {
			var n int
			if _, err := fmt.Sscanf(val, "%d", &n); err == nil {
				v.Set(cfgKey, n)
			}
		}
	}

	durationKeys := map[string]string{
		"TANGO_MONGO_MAXELAPSEDTIME":         "mongo.maxElapsedTime",
		"TANGO_MONGO_CONNECTTIMEOUT":         "mongo.connectTimeout",
		"TANGO_MONGO_SERVERSELECTIONTIMEOUT": "mongo.serverSelectionTimeout",
		"TANGO_SOURCE_RESCANINTERVAL":        "source.rescanInterval",
		"TANGO_SOURCE_POLLINTERVAL":          "source.pollInterval",
		"TANGO_PIPELINE_FLUSH_INTERVAL":      "pipeline.flushInterval",
		"TANGO_REMOTE_CONFIG_SYNC_INTERVAL":  "remoteConfig.syncInterval",
	}
	for envKey, cfgKey := range durationKeys {
		if val := os.Getenv(envKey); val != "" {
			if d, err := time.ParseDuration(val); err == nil {
				v.Set(cfgKey, d)
			}
		}
	}

	boolKeys := map[string]string{
		"TANGO_REMOTE_CONFIG_ENABLED": "remoteConfig.enabled",
	}
	for envKey, cfgKey := range boolKeys {
		if val := os.Getenv(envKey); val != "" {
			if val == "true" || val == "1" {
				v.Set(cfgKey, true)
			} else if val == "false" || val == "0" {
				v.Set(cfgKey, false)
			}
		}
	}

	stringKeys := map[string]string{
		"TANGO_MODE":                      "mode",
		"TANGO_LOGGING_LEVEL":             "logging.level",
		"TANGO_LOGGING_FORMAT":            "logging.format",
		"TANGO_MONGO_URI":                 "mongo.uri",
		"TANGO_SOURCE_TAILMODE":           "source.tailMode",
		"TANGO_REMOTE_CONFIG_COLLECTION":  "remoteConfig.collection",
		"TANGO_REMOTE_CONFIG_DOCUMENT_ID": "remoteConfig.documentID",
	}
	for envKey, cfgKey := range stringKeys {
		if val := os.Getenv(envKey); val != "" {
			v.Set(cfgKey, val)
		}
	}
}

// setDefaults registers viper defaults for all runtime fields.
func setDefaults(v *viper.Viper) {
	v.SetDefault("mode", ModeDaemon)
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "text")
	v.SetDefault("mongo.uri", "")
	v.SetDefault("mongo.maxElapsedTime", "10s")
	v.SetDefault("mongo.connectTimeout", "10s")
	v.SetDefault("mongo.serverSelectionTimeout", "30s")
	v.SetDefault("source.logPattern", []string{})
	v.SetDefault("source.tailMode", TailModeHybrid)
	v.SetDefault("source.rescanInterval", "30s")
	v.SetDefault("source.pollInterval", "200ms")
	v.SetDefault("source.maxLineBytes", 10*1024*1024)
	v.SetDefault("pipeline.batchSize", 1000)
	v.SetDefault("pipeline.batchSizeMin", 0)
	v.SetDefault("pipeline.batchSizeMax", 0)
	v.SetDefault("pipeline.batchWorkers", 2)
	v.SetDefault("pipeline.flushInterval", "1s")
	v.SetDefault("pipeline.channelBuffer", 0)
	v.SetDefault("pipeline.deadLetterCap", 128)
	v.SetDefault("filter.include", []string{})
	v.SetDefault("filter.exclude", []string{})
}

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(c *Config) {
	if c.Mode == "" {
		c.Mode = ModeDaemon
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "text"
	}
	if c.Mongo.MaxElapsedTime <= 0 {
		c.Mongo.MaxElapsedTime = 10 * time.Second
	}
	if c.Mongo.ConnectTimeout <= 0 {
		c.Mongo.ConnectTimeout = 10 * time.Second
	}
	if c.Mongo.ServerSelectionTimeout <= 0 {
		c.Mongo.ServerSelectionTimeout = 30 * time.Second
	}
	if c.Source.RescanInterval <= 0 {
		c.Source.RescanInterval = 30 * time.Second
	}
	if c.Source.TailMode == "" {
		c.Source.TailMode = TailModeHybrid
	}
	if c.Source.PollInterval <= 0 {
		c.Source.PollInterval = 200 * time.Millisecond
	}
	if c.Source.MaxLineBytes <= 0 {
		c.Source.MaxLineBytes = 10 * 1024 * 1024
	}
	if c.Pipeline.BatchSize <= 0 {
		c.Pipeline.BatchSize = 1000
	}
	if c.Pipeline.BatchSizeMin > 0 && c.Pipeline.BatchSizeMin > c.Pipeline.BatchSize {
		c.Pipeline.BatchSizeMin = c.Pipeline.BatchSize
	}
	if c.Pipeline.BatchSizeMax > 0 && c.Pipeline.BatchSizeMax < c.Pipeline.BatchSize {
		c.Pipeline.BatchSizeMax = c.Pipeline.BatchSize
	}
	if c.Pipeline.BatchWorkers <= 0 {
		c.Pipeline.BatchWorkers = 2
	}
	if c.Pipeline.FlushInterval <= 0 {
		c.Pipeline.FlushInterval = time.Second
	}
	if c.Pipeline.DeadLetterCap <= 0 {
		c.Pipeline.DeadLetterCap = 128
	}
	applyRemoteConfigDefaults(&c.RemoteConfig)
}

func applyRemoteConfigDefaults(rc *RemoteConfig) {
	if rc.Collection == "" {
		rc.Collection = DefaultRemoteConfigCollection
	}
	if rc.DocumentID == "" {
		rc.DocumentID = DefaultRemoteConfigDocumentID
	}
	if rc.SyncInterval <= 0 {
		rc.SyncInterval = DefaultRemoteConfigInterval
	}
}

// Validate checks that required fields are present and well-formed.
func (c *Config) Validate() error {
	if c.Mode != ModeDaemon {
		return fmt.Errorf("config: mode must be %q; got %q", ModeDaemon, c.Mode)
	}
	if c.Mongo.URI == "" {
		return fmt.Errorf("config: mongo.uri is required (daemon: generic.mongo.uri — set in the config file, via env, or via flag)")
	}
	switch c.Source.TailMode {
	case TailModeHybrid, TailModePoll, TailModeEvent:
	default:
		return fmt.Errorf("config: source.tailMode must be %q, %q or %q; got %q",
			TailModeHybrid, TailModePoll, TailModeEvent, c.Source.TailMode)
	}
	if c.Pipeline.BatchSizeMin > 0 && c.Pipeline.BatchSizeMin > c.Pipeline.BatchSize {
		return fmt.Errorf("config: pipeline.batchSizeMin (%d) cannot exceed pipeline.batchSize (%d)",
			c.Pipeline.BatchSizeMin, c.Pipeline.BatchSize)
	}
	if c.Pipeline.BatchSizeMax > 0 && c.Pipeline.BatchSize > c.Pipeline.BatchSizeMax {
		return fmt.Errorf("config: pipeline.batchSize (%d) cannot exceed pipeline.batchSizeMax (%d)",
			c.Pipeline.BatchSize, c.Pipeline.BatchSizeMax)
	}
	if _, err := filter.New(c.Filter.Include, c.Filter.Exclude); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// BuildFilter compiles the configured reporting filter. Validate must have been
// called first.
func (c *Config) BuildFilter() (*filter.Filter, error) {
	return filter.New(c.Filter.Include, c.Filter.Exclude)
}

// MongoDBFromURI extracts the database name from a MongoDB URI path.
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
