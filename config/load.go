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
// works (a TANGO_* var only overrides a key viper already knows) and unset
// values stay zero so each module's ApplyDefaults can normalise them. The key
// paths mirror the package paths under internal/.
func setDefaults(v *viper.Viper) {
	// logging -> internal/logging
	v.SetDefault("logging.level", "")
	v.SetDefault("logging.format", "")
	// dao -> internal/dao{,/mongo,/store}
	v.SetDefault("dao.mongo.uri", "")
	v.SetDefault("dao.mongo.connectTimeout", "0s")
	v.SetDefault("dao.mongo.serverSelectionTimeout", "0s")
	v.SetDefault("dao.store.maxElapsedTime", "0s")
	// parser -> internal/parser/filter
	v.SetDefault("parser.filter.include", []string{})
	v.SetDefault("parser.filter.exclude", []string{})
	// source -> internal/source/tailer
	v.SetDefault("source.tailer.logPattern", []string{})
	v.SetDefault("source.tailer.tailMode", "")
	v.SetDefault("source.tailer.rescanInterval", "0s")
	v.SetDefault("source.tailer.pollInterval", "0s")
	v.SetDefault("source.tailer.maxLineBytes", 0)
	// process -> internal/process{,/pipeline}
	v.SetDefault("process.batchSize", 0)
	setPipelineDefaults(v, "process.pipeline")
	// role.gateway -> internal/role/gateway
	v.SetDefault("role.gateway.addr", "")
	v.SetDefault("role.gateway.upload.defaultMode", "")
	v.SetDefault("role.gateway.upload.batchSize", 0)
	setPipelineDefaults(v, "role.gateway.upload.pipeline")
	v.SetDefault("role.gateway.upload.filter.include", []string{})
	v.SetDefault("role.gateway.upload.filter.exclude", []string{})
}

// setPipelineDefaults registers the pipeline.Config keys under the given prefix
// (the pipeline config appears both at process.pipeline and inside the gateway
// upload config).
func setPipelineDefaults(v *viper.Viper, prefix string) {
	v.SetDefault(prefix+".batchSize", 0)
	v.SetDefault(prefix+".batchSizeMin", 0)
	v.SetDefault(prefix+".batchSizeMax", 0)
	v.SetDefault(prefix+".batchWorkers", 0)
	v.SetDefault(prefix+".flushInterval", "0s")
	v.SetDefault(prefix+".channelBuffer", 0)
	v.SetDefault(prefix+".deadLetterCap", 0)
}
