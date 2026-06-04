// Package config is the central configuration entry point. It builds a
// fully-resolved viper from built-in defaults + config file (YAML or JSON, by
// extension) + TANGO_* environment variables + CLI flags, materialises it, and
// hands back a dependency-free cfgtree.Tree.
//
// There is no top-level aggregate Config struct: this package owns no field
// definitions. Each module owns its own section and decodes it from the Tree via
// the module's FromTree (which runs that module's ApplyDefaults/Validate). The
// only thing centralised here is the list of top-level sections — registerAll —
// needed up front so env binding and CLI flags know every key.
//
// Sources, in increasing priority: defaults < config file < TANGO_* env < flags.
package config

import (
	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/role"
	"rocket-nano/tools/tango/internal/source"
)

// registerAll registers every module section's config keys (under its section
// prefix) with set. It is the single place that knows the top-level section
// list; each module owns the keys beneath its own prefix via RegisterDefaults.
// Used both to seed viper defaults (so AutomaticEnv binds every key) and to
// define the matching CLI flags.
func registerAll(set func(key string, value any)) {
	new(logging.Config).RegisterDefaults(set, "logging")
	new(dao.Config).RegisterDefaults(set, "dao")
	new(parser.Config).RegisterDefaults(set, "parser")
	new(source.Config).RegisterDefaults(set, "source")
	new(process.Config).RegisterDefaults(set, "process")
	new(role.Config).RegisterDefaults(set, "role")
}
