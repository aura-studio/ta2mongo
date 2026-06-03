// Package config defines tango's configuration.
//
// There is a single, package-path-aligned schema (Config): every file key maps
// to the package path that consumes it —
//
//	logging.level                 -> internal/logging
//	dao.mongo.uri                 -> internal/dao/mongo
//	dao.store.maxElapsedTime      -> internal/dao/store
//	parser.filter.include         -> internal/parser/filter
//	source.tailer.logPattern      -> internal/source/tailer
//	process.pipeline.batchSize    -> internal/process/pipeline
//	role.gateway.addr             -> internal/role/gateway
//
// The config package owns no field definitions: it only aggregates each
// module's own Config struct and runs the load/override mechanics (loader.go).
// Specific configuration (fields, defaults, validation knobs) lives in the
// internal sub-modules. Each role/command picks the sections it needs.
//
// Sources, in increasing priority: built-in defaults < config file (YAML or
// JSON, by extension) < TANGO_* environment variables < CLI flags.
package config

import (
	"fmt"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role"
	"rocket-nano/tools/tango/internal/source"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// Config is the unified configuration schema. Each section is a pointer to the
// owning module's own config struct, so the file key path equals the package
// path under internal/.
type Config struct {
	Logging *logging.Config `mapstructure:"logging"`
	Dao     *dao.Config     `mapstructure:"dao"`
	Parser  *parser.Config  `mapstructure:"parser"`
	Source  *source.Config  `mapstructure:"source"`
	Process *process.Config `mapstructure:"process"`
	Role    *role.Config    `mapstructure:"role"`
}

// Validate checks fields required by the daemon report pipeline. It assumes
// applyDefaults has run (section pointers non-nil) but guards defensively.
func (c *Config) Validate() error {
	if c.Dao == nil || c.Dao.Mongo == nil || c.Dao.Mongo.URI == "" {
		return fmt.Errorf("config: dao.mongo.uri is required (set it in the config file, via TANGO_DAO_MONGO_URI, or via --dao.mongo.uri)")
	}
	if c.Source == nil || c.Source.Tailer == nil {
		return fmt.Errorf("config: source.tailer configuration is required")
	}
	switch c.Source.Tailer.TailMode {
	case tailer.TailModeHybrid, tailer.TailModePoll, tailer.TailModeEvent:
		// valid
	default:
		return fmt.Errorf("config: source.tailer.tailMode must be %q, %q or %q; got %q",
			tailer.TailModeHybrid, tailer.TailModePoll, tailer.TailModeEvent, c.Source.Tailer.TailMode)
	}
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
	if c.Parser != nil {
		if _, err := c.Parser.Build(); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	return nil
}
