package config

import (
	"github.com/spf13/pflag"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// Load reads the unified config from path (+ TANGO_* env + CLI flags) and returns
// a dependency-free cfgtree.Tree that every module slices its own branch from.
// An empty or missing path is fine — defaults + env + flags still produce a
// usable Tree. There is no top-level aggregate Config: each module owns its
// section and decodes it (with its own ApplyDefaults/Validate) via FromTree.
func Load(path string, flags *pflag.FlagSet) (cfgtree.Tree, error) {
	v := newViper()
	registerAll(v.SetDefault)
	if err := readConfigFile(v, path); err != nil {
		return cfgtree.Tree{}, err
	}
	if err := bindFlagsTo(v, flags); err != nil {
		return cfgtree.Tree{}, err
	}

	// Materialise: AllSettings resolves every registered key through the
	// default < file < env < flag precedence into a plain nested map. cfgtree
	// then slices and decodes branches of that map, so the modules never depend
	// on viper. (Slicing the live viper would drop env/flag-only leaf values,
	// hence the up-front materialisation.)
	return cfgtree.New(v.AllSettings()), nil
}

// RegisterFlags defines a CLI flag for every config key, named identically to the
// dotted key path (e.g. --dao.mongo.uri), so the three input paths are fully
// interchangeable: config file value, TANGO_* env var, and --<key> flag all set
// the same key. Precedence (low→high) is default < file < env < flag — only a
// flag the user actually sets overrides file/env (see bindFlagsTo).
//
// The --config file path is registered separately by the root command and is not
// a config key. The key list is owned by the modules (registerAll), so this
// enumerates nothing itself.
func RegisterFlags(flags *pflag.FlagSet) {
	registerAll(func(key string, _ any) {
		if flags.Lookup(key) == nil {
			flags.String(key, "", "config key "+key+" (overrides file / TANGO_* env)")
		}
	})
}
