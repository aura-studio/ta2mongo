package config

import (
	"fmt"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Load reads the unified config from path (+ TANGO_* env + CLI flags), applies
// per-module defaults, and returns the populated Config. An empty or missing
// path is fine — defaults + env + flags still produce a usable Config. Each
// role/command picks the sections it needs and validates accordingly.
func Load(path string, flags *pflag.FlagSet) (*Config, error) {
	v := newViper()
	setDefaults(v)
	if err := readConfigFile(v, path); err != nil {
		return nil, err
	}
	if err := bindFlagsTo(v, flags); err != nil {
		return nil, err
	}

	var c Config
	if err := v.Unmarshal(&c, durationDecodeHook(), weaklyTyped()); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	applyDefaults(&c)
	return &c, nil
}

// setDefaults registers every config key with viper so AutomaticEnv binding
// works (a TANGO_* var only overrides a key viper already knows). The actual key
// list is owned by the modules: Config.RegisterDefaults cascades down each
// section's own RegisterDefaults, so this package enumerates no keys itself.
func setDefaults(v *viper.Viper) {
	(&Config{}).RegisterDefaults(v.SetDefault)
}

// RegisterFlags defines a CLI flag for every config key, named identically to
// the dotted key path (e.g. --dao.mongo.uri), so the three input paths are fully
// interchangeable: config file value, TANGO_* env var, and --<key> flag all set
// the same key. Precedence (low→high) is default < file < env < flag — only a
// flag the user actually sets overrides file/env (see bindFlagsTo).
//
// The runtime role is chosen by the subcommand, not a flag; the --config file
// path is registered separately by the root command. Both are intentionally not
// config keys. The key list is owned by the modules (same cascade as
// setDefaults), so this enumerates nothing itself.
func RegisterFlags(flags *pflag.FlagSet) {
	(&Config{}).RegisterDefaults(func(key string, _ any) {
		if flags.Lookup(key) == nil {
			flags.String(key, "", "config key "+key+" (overrides file / TANGO_* env)")
		}
	})
}
