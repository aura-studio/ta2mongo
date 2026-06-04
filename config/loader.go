package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// newViper builds a viper instance with TANGO_* environment binding enabled.
// AutomaticEnv binds a key like "mongo.uri" to TANGO_MONGO_URI as long as the
// key is known to viper (registered via a default), which the per-config
// setXDefaults functions ensure.
func newViper() *viper.Viper {
	v := viper.New()
	v.SetEnvPrefix("TANGO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	return v
}

// readConfigFile reads the config file at path into v, inferring YAML vs JSON
// from the extension. An empty or non-existent path is skipped silently so a
// pure env/flag configuration still works.
func readConfigFile(v *viper.Viper, path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat config %q: %w", path, err)
	}
	v.SetConfigFile(path) // extension (.yaml/.yml/.json) selects the parser
	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("read config %q: %w", path, err)
	}
	return nil
}

// bindFlagsTo binds the flags that were explicitly set on the CLI to the viper
// key of the SAME name, using viper's native hierarchical model: a flag named
// "generic.mongo.uri" sets the "generic.mongo.uri" key directly — no alias
// table. The special --config flag is a file path, not a config key, so it is
// skipped. Unset flags fall back to file/env/default.
func bindFlagsTo(v *viper.Viper, flags *pflag.FlagSet) error {
	if flags == nil {
		return nil
	}
	var bindErr error
	flags.Visit(func(f *pflag.Flag) {
		if bindErr != nil || f.Name == "config" {
			return
		}
		if err := v.BindPFlag(f.Name, f); err != nil {
			bindErr = fmt.Errorf("bind flag %q: %w", f.Name, err)
		}
	})
	return bindErr
}
