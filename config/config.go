// Package config defines tango's configuration.
//
// The single file-facing schema is RoleConfig (role.go): the unified
// standalone/gateway config file. Its loaders project it onto the shared
// runtime Config in this file (standalone) and onto ClientConfig (client.go,
// the gateway runtime projection). The internal service packages
// (daemon/gateway) consume those. loader.go holds the shared YAML/JSON +
// TANGO_* env + flag loading helpers.
//
// Each runtime concern owns its own config struct in its module (mongo.Config,
// store.Config, tailer.Config, pipeline.Config, filter.Config, log.Config); the
// top-level Config merely references them by pointer.
//
// Sources, in increasing priority: built-in defaults < config file (YAML or
// JSON, by extension) < TANGO_* environment variables < CLI flags.
package config

import (
	"fmt"

	"rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/log"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process/pipeline"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// Mode constants identify the runtime role a Config drives. Mode is an internal
// runtime distinction consumed only by Validate; the role loaders set it from
// the command that built the Config.
const (
	// ModeReport is the report service runtime: tail logs -> filter -> Mongo.
	ModeReport = "report"
)

// Config is the top-level runtime configuration for the report pipeline. Each
// section is a pointer to the owning module's config struct. YAML keys are the
// mapstructure tags; env vars use the TANGO_ prefix with "." → "_"
// (e.g. mongo.uri → TANGO_MONGO_URI).
type Config struct {
	// Mode selects the runtime role. Only report is supported.
	Mode string `mapstructure:"mode"`

	// Logging configures log output (internal/log).
	Logging *log.Config `mapstructure:"logging"`

	// Mongo configures the MongoDB connection (internal/dao/mongo).
	Mongo *mongo.Config `mapstructure:"mongo"`

	// Store configures the store's write-retry behaviour (internal/dao/store).
	Store *store.Config `mapstructure:"store"`

	// Source configures the file-tailing data source (internal/source/tailer).
	Source *tailer.Config `mapstructure:"source"`

	// Pipeline configures batching and parallel write workers
	// (internal/process/pipeline).
	Pipeline *pipeline.Config `mapstructure:"pipeline"`

	// Filter configures the reporting filter (internal/parser/filter).
	Filter *filter.Config `mapstructure:"filter"`
}

// Validate checks that required fields are present. It assumes applyDefaults has
// run (so the section pointers are non-nil) but still guards them defensively.
func (c *Config) Validate() error {
	if c.Mode != ModeReport {
		return fmt.Errorf("config: mode must be %q; got %q", ModeReport, c.Mode)
	}
	if c.Mongo == nil || c.Mongo.URI == "" {
		return fmt.Errorf("config: mongo.uri is required (set runtime.mongo.uri in the config file, via TANGO_RUNTIME_MONGO_URI, or via --runtime.mongo.uri)")
	}
	if c.Source == nil {
		return fmt.Errorf("config: source configuration is required")
	}
	switch c.Source.TailMode {
	case tailer.TailModeHybrid, tailer.TailModePoll, tailer.TailModeEvent:
		// valid
	default:
		return fmt.Errorf("config: source.tailMode must be %q, %q or %q; got %q",
			tailer.TailModeHybrid, tailer.TailModePoll, tailer.TailModeEvent, c.Source.TailMode)
	}
	// Validate batch size constraints.
	if c.Pipeline != nil {
		if c.Pipeline.BatchSizeMin > 0 && c.Pipeline.BatchSizeMin > c.Pipeline.BatchSize {
			return fmt.Errorf("config: pipeline.batchSizeMin (%d) cannot exceed pipeline.batchSize (%d)",
				c.Pipeline.BatchSizeMin, c.Pipeline.BatchSize)
		}
		if c.Pipeline.BatchSizeMax > 0 && c.Pipeline.BatchSize > c.Pipeline.BatchSizeMax {
			return fmt.Errorf("config: pipeline.batchSize (%d) cannot exceed pipeline.batchSizeMax (%d)",
				c.Pipeline.BatchSize, c.Pipeline.BatchSizeMax)
		}
	}
	if _, err := c.Filter.Build(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}
