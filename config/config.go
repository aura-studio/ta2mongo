// Package config defines tango's configuration.
//
// The single file-facing schema is RoleConfig (role.go): the unified
// standalone/gateway config file. Its loaders project it onto the shared
// runtime Config in this file (standalone) and onto ClientConfig (client.go,
// the gateway runtime projection). The internal service packages
// (daemon/gateway) consume those. loader.go holds the shared YAML/JSON +
// TANGO_* env + flag loading helpers.
//
// Each runtime concern owns its own config struct in its module (dao.Config,
// engine.Config, logging.Config, parser.Config, process.Config, tailer.Config);
// the top-level Config merely references them by pointer.
//
// Sources, in increasing priority: built-in defaults < config file (YAML or
// JSON, by extension) < TANGO_* environment variables < CLI flags.
package config

import (
	"fmt"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/engine"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
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
// section is a pointer to the owning module's config struct.
type Config struct {
	// Dao configures data access: MongoDB connection and store write behaviour.
	Dao *dao.Config `mapstructure:"dao"`

	// Engine configures runtime engine behavior.
	Engine *engine.Config `mapstructure:"engine"`

	// Logging configures log output (internal/logging).
	Logging *logging.Config `mapstructure:"logging"`

	// Parser configures log parsing and reporting filters.
	Parser *parser.Config `mapstructure:"parser"`

	// Process configures ingestion processing.
	Process *process.Config `mapstructure:"process"`

	// Source configures the file-tailing data source (internal/source/tailer).
	Source *tailer.Config `mapstructure:"source"`
}

// Validate checks that required fields are present. It assumes applyDefaults has
// run (so the section pointers are non-nil) but still guards them defensively.
func (c *Config) Validate() error {
	if c.Engine == nil || c.Engine.Mode != ModeReport {
		got := ""
		if c.Engine != nil {
			got = c.Engine.Mode
		}
		return fmt.Errorf("config: engine.mode must be %q; got %q", ModeReport, got)
	}
	if c.Dao == nil || c.Dao.Mongo == nil || c.Dao.Mongo.URI == "" {
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
	if c.Process != nil && c.Process.Pipeline != nil {
		p := c.Process.Pipeline
		if p.BatchSizeMin > 0 && p.BatchSizeMin > p.BatchSize {
			return fmt.Errorf("config: process.pipeline.batchSizeMin (%d) cannot exceed process.pipeline.batchSize (%d)",
				p.BatchSizeMin, p.BatchSize)
		}
		if p.BatchSizeMax > 0 && p.BatchSize > p.BatchSizeMax {
			return fmt.Errorf("config: process.pipeline.batchSize (%d) cannot exceed process.pipeline.batchSizeMax (%d)",
				p.BatchSize, p.BatchSizeMax)
		}
	}
	if _, err := c.Parser.Build(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}
