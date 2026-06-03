// Package config defines tango's configuration.
//
// The single file-facing schema is RoleConfig (role.go): the unified
// standalone/gateway config file. Its loaders project it onto the shared
// runtime Config in this file (standalone) and onto ClientConfig (client.go,
// the gateway runtime projection). The internal service packages
// (report/gateway) consume those. loader.go holds the shared YAML/JSON +
// TANGO_* env + flag loading helpers.
//
// Sources, in increasing priority: built-in defaults < config file (YAML or
// JSON, by extension) < TANGO_* environment variables < CLI flags.
package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"rocket-nano/tools/tango/internal/core/filter"
)

// Mode constants identify the runtime role a Config drives. Mode is an internal
// runtime distinction consumed only by Validate; the role loaders set it from
// the command that built the Config.
const (
	// ModeReport is the report service runtime: tail logs -> filter -> Mongo.
	ModeReport = "report"
)

// TailMode constants control how the tailer watches for file changes.
const (
	// TailModeHybrid uses hpcloud/tail's event-driven watcher as the
	// primary mechanism, with a periodic poll fallback that detects missed
	// notifications. Combines low latency with reliability.
	TailModeHybrid = "hybrid"

	// TailModePoll uses a simple polling loop (read → sleep → retry).
	// Immune to notification-drop races; suitable for all workloads.
	TailModePoll = "poll"

	// TailModeEvent uses hpcloud/tail's kqueue/inotify event-driven watcher.
	// Lowest latency but may stall under sustained concurrent writes due to
	// a known sendOnlyIfEmpty race in the upstream library.
	TailModeEvent = "event"
)

// Config is the top-level runtime configuration for the report pipeline.
// Settings are grouped by concern into nested sections (logging, mongo, source,
// pipeline, filter). YAML keys are the mapstructure tags; env vars use the
// TANGO_ prefix with "." → "_" (e.g. mongo.uri → TANGO_MONGO_URI).
type Config struct {
	// Mode selects the runtime role. Only report is supported.
	Mode string `mapstructure:"mode"`

	// Logging configures log output.
	Logging LoggingConfig `mapstructure:"logging"`

	// Mongo configures the MongoDB connection and write-retry behaviour.
	Mongo MongoConfig `mapstructure:"mongo"`

	// Source configures the file-tailing data source (report service).
	Source SourceConfig `mapstructure:"source"`

	// Pipeline configures batching and parallel write workers.
	Pipeline PipelineConfig `mapstructure:"pipeline"`

	// Filter configures the reporting (upload) filter: expr-lang include/exclude
	// rules applied to every record by the report / file-upload / string-upload
	// paths.
	Filter FilterConfig `mapstructure:"filter"`
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
	// Default 10s.
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`
	// ConnectTimeout bounds the initial connection handshake. Default 10s.
	ConnectTimeout time.Duration `mapstructure:"connectTimeout"`
	// ServerSelectionTimeout bounds how long the driver waits for a suitable
	// server before failing an operation. Default 30s.
	ServerSelectionTimeout time.Duration `mapstructure:"serverSelectionTimeout"`
}

// SourceConfig configures the file-tailing data source (report service).
type SourceConfig struct {
	// LogPattern is a list of glob/regex patterns matched against file paths.
	// Required for the report service.
	LogPattern []string `mapstructure:"logPattern"`
	// TailMode selects the file-tailing strategy: hybrid (default) / poll / event.
	TailMode string `mapstructure:"tailMode"`
	// RescanInterval is how often the tailer rescans for new files.
	// Default 30s.
	RescanInterval time.Duration `mapstructure:"rescanInterval"`
	// PollInterval is the poll cadence used by poll / hybrid tail modes.
	// Default 200ms.
	PollInterval time.Duration `mapstructure:"pollInterval"`
	// MaxLineBytes caps a single log line's length. Default 10485760 (10 MB).
	MaxLineBytes int `mapstructure:"maxLineBytes"`
}

// PipelineConfig configures batching and parallel write workers.
type PipelineConfig struct {
	// BatchSize is the target number of records per bulk-write flush.
	// The adaptive min/max can be overridden via batchSizeMin/batchSizeMax.
	// Default 1000.
	BatchSize int `mapstructure:"batchSize"`
	// BatchSizeMin is the adaptive lower bound for batch sizing.
	// When 0, it is auto-derived as BatchSize/4 (minimum 1).
	BatchSizeMin int `mapstructure:"batchSizeMin"`
	// BatchSizeMax is the adaptive upper bound for batch sizing.
	// When 0, it is auto-derived as BatchSize*2.
	BatchSizeMax int `mapstructure:"batchSizeMax"`
	// BatchWorkers is the number of parallel write workers. Default 2.
	BatchWorkers int `mapstructure:"batchWorkers"`
	// FlushInterval is how often workers flush partial batches. Default 1s.
	FlushInterval time.Duration `mapstructure:"flushInterval"`
	// ChannelBuffer is the per-worker line-channel buffer. Default 0 means
	// "derive as BatchSize*2".
	ChannelBuffer int `mapstructure:"channelBuffer"`
	// DeadLetterCap is the per-worker dead-letter batch capacity. Default 128.
	DeadLetterCap int `mapstructure:"deadLetterCap"`
}

// FilterConfig configures the expr-lang include/exclude rules.
type FilterConfig struct {
	// Include is a list of expr-lang expressions. If non-empty, a parsed
	// record is kept only when at least one expression evaluates to true
	// (OR semantics). An empty list lets every record through this stage.
	// Expressions are evaluated against the flattened record document, so
	// fields like "#type", "#event_name", and any "properties.*" keys are
	// accessible at the top level (e.g. `#type == "track"`).
	Include []string `mapstructure:"include"`
	// Exclude is a list of expr-lang expressions. A parsed record is dropped
	// if any expression evaluates to true. Applied after Include.
	Exclude []string `mapstructure:"exclude"`
}

// Validate checks that required fields are present.
func (c *Config) Validate() error {
	if c.Mode != ModeReport {
		return fmt.Errorf("config: mode must be %q; got %q", ModeReport, c.Mode)
	}
	if c.Mongo.URI == "" {
		return fmt.Errorf("config: mongo.uri is required (set runtime.mongo.uri in the config file, via TANGO_RUNTIME_MONGO_URI, or via --runtime.mongo.uri)")
	}
	switch c.Source.TailMode {
	case TailModeHybrid, TailModePoll, TailModeEvent:
		// valid
	default:
		return fmt.Errorf("config: source.tailMode must be %q, %q or %q; got %q",
			TailModeHybrid, TailModePoll, TailModeEvent, c.Source.TailMode)
	}
	// Validate batch size constraints.
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
