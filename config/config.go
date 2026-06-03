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

// RegisterDefaults cascades default-key registration to each section, owning
// only the top-level segment names (its own field tags) and delegating the rest.
// set is typically viper's SetDefault, used so AutomaticEnv binds every key.
func (c *Config) RegisterDefaults(set func(key string, value any)) {
	new(logging.Config).RegisterDefaults(set, "logging")
	new(dao.Config).RegisterDefaults(set, "dao")
	new(parser.Config).RegisterDefaults(set, "parser")
	new(source.Config).RegisterDefaults(set, "source")
	new(process.Config).RegisterDefaults(set, "process")
	new(role.Config).RegisterDefaults(set, "role")
}

// Validate validates the whole configuration by delegating to each present
// section's own Validate. The config package owns no validation rules of its
// own — they live in the modules. The wrapped error path mirrors the key path
// (e.g. "dao: mongo: uri is required"). It assumes applyDefaults has run but
// guards nil sections defensively.
func (c *Config) Validate() error {
	if c.Logging != nil {
		if err := c.Logging.Validate(); err != nil {
			return fmt.Errorf("logging: %w", err)
		}
	}
	if c.Dao != nil {
		if err := c.Dao.Validate(); err != nil {
			return fmt.Errorf("dao: %w", err)
		}
	}
	if c.Parser != nil {
		if err := c.Parser.Validate(); err != nil {
			return fmt.Errorf("parser: %w", err)
		}
	}
	if c.Source != nil {
		if err := c.Source.Validate(); err != nil {
			return fmt.Errorf("source: %w", err)
		}
	}
	if c.Process != nil {
		if err := c.Process.Validate(); err != nil {
			return fmt.Errorf("process: %w", err)
		}
	}
	if c.Role != nil {
		if err := c.Role.Validate(); err != nil {
			return fmt.Errorf("role: %w", err)
		}
	}
	return nil
}
