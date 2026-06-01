package config

import (
	"fmt"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Daemon run modes, selected by the daemon subcommand
// (`tango daemon standalone` / `tango daemon cluster`).
const (
	// DaemonModeStandalone is the self-contained reporting daemon: tail logs →
	// parse → report filter → write to Mongo. The filter is local; it does NOT
	// pull remote config.
	DaemonModeStandalone = "standalone"
	// DaemonModeCluster does everything standalone does and additionally syncs
	// the report filter from the remote-config document (hot-reload), so a fleet
	// of daemons converges on a centrally-managed filter.
	DaemonModeCluster = "cluster"
)

// DaemonConfig is the schema of the daemon config file. It is organised into
// two parts:
//
//   - generic: process-wide settings shared by both modes (logging + the
//     MongoDB connection).
//   - report:  the reporting pipeline — tail logs → parse → report filter →
//     write to Mongo. report.filter has two sources: local (this file) and
//     remote (a MongoDB document, consulted only in cluster mode, that
//     hot-reloads the filter from a control plane).
//
// There is no enabled switch: the active mode is chosen by the subcommand, not
// the file.
type DaemonConfig struct {
	Generic GenericConfig      `mapstructure:"generic"`
	Report  DaemonReportConfig `mapstructure:"report"`
}

// GenericConfig holds the process-wide settings shared by both modes: log output
// and the MongoDB connection (both modes write through the same connection).
type GenericConfig struct {
	Logging LoggingConfig `mapstructure:"logging"`
	Mongo   MongoConfig   `mapstructure:"mongo"`
}

// DaemonReportConfig is the reporting pipeline block, used by both modes. The
// daemon tails logPattern files, applies the report filter, and writes to Mongo.
type DaemonReportConfig struct {
	Source   SourceConfig       `mapstructure:"source"`
	Pipeline PipelineConfig     `mapstructure:"pipeline"`
	Filter   DaemonFilterConfig `mapstructure:"filter"`
}

// DaemonFilterConfig groups the two filter sources under report.filter:
//   - local:  the include/exclude rules from this config file.
//   - remote: the MongoDB control-plane document that, in cluster mode,
//     overrides and hot-reloads the local rules. Consulted only in cluster
//     mode; standalone keeps the local filter verbatim.
type DaemonFilterConfig struct {
	Local  FilterConfig `mapstructure:"local"`
	Remote RemoteConfig `mapstructure:"remote"`
}

// ToRuntime projects the file-facing daemon config onto the internal runtime
// Config. The mode-derived switch (RemoteConfig.Enabled) is applied by
// LoadDaemon, not here.
func (d DaemonConfig) ToRuntime() Config {
	return Config{
		Mode:         ModeDaemon,
		Logging:      d.Generic.Logging,
		Mongo:        d.Generic.Mongo,
		Source:       d.Report.Source,
		Pipeline:     d.Report.Pipeline,
		Filter:       d.Report.Filter.Local,
		RemoteConfig: d.Report.Filter.Remote,
	}
}

// LoadDaemon loads and validates a daemon config for the given run mode
// (DaemonModeStandalone / DaemonModeCluster) from path (+ env + flags),
// returning both the file-facing config and the projected runtime Config with
// defaults and the mode-derived switch applied. An empty/absent path falls back
// to defaults + env + flags.
func LoadDaemon(path string, flags *pflag.FlagSet, mode string) (DaemonConfig, Config, error) {
	switch mode {
	case DaemonModeStandalone, DaemonModeCluster:
		// valid
	default:
		return DaemonConfig{}, Config{}, fmt.Errorf("config: unknown daemon mode %q (want %q or %q)", mode, DaemonModeStandalone, DaemonModeCluster)
	}

	v := newViper()
	setDaemonDefaults(v)
	if err := readConfigFile(v, path); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	if err := bindFlagsTo(v, flags); err != nil {
		return DaemonConfig{}, Config{}, err
	}

	var dc DaemonConfig
	if err := v.Unmarshal(&dc, durationDecodeHook()); err != nil {
		return DaemonConfig{}, Config{}, fmt.Errorf("unmarshal daemon config: %w", err)
	}

	rt := dc.ToRuntime()
	applyDefaults(&rt)

	// Cluster mode enables the control-plane filter sync; standalone keeps it off.
	rt.RemoteConfig.Enabled = mode == DaemonModeCluster

	if err := rt.Validate(); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	// Both modes report, so a log pattern is always required — fail fast at load
	// time rather than at pipeline start.
	if len(dc.Report.Source.LogPattern) == 0 {
		return DaemonConfig{}, Config{}, fmt.Errorf("config: report.source.logPattern is required (at least one regex)")
	}
	return dc, rt, nil
}

// setDaemonDefaults registers viper defaults for the daemon schema so that
// AutomaticEnv binding works for every key and unset values are sane. Env vars
// follow the nested key with TANGO_ prefix and "." → "_" (e.g.
// generic.mongo.uri → TANGO_GENERIC_MONGO_URI).
func setDaemonDefaults(v *viper.Viper) {
	v.SetDefault("generic.logging.level", "info")
	v.SetDefault("generic.logging.format", "text")
	v.SetDefault("generic.mongo.uri", "")
	v.SetDefault("generic.mongo.maxElapsedTime", "10s")
	v.SetDefault("generic.mongo.connectTimeout", "10s")
	v.SetDefault("generic.mongo.serverSelectionTimeout", "30s")
	v.SetDefault("report.source.logPattern", []string{})
	v.SetDefault("report.source.tailMode", TailModeHybrid)
	v.SetDefault("report.source.rescanInterval", "30s")
	v.SetDefault("report.source.pollInterval", "200ms")
	v.SetDefault("report.source.maxLineBytes", 10*1024*1024)
	v.SetDefault("report.pipeline.batchSize", 1000)
	v.SetDefault("report.pipeline.batchWorkers", 2)
	v.SetDefault("report.pipeline.flushInterval", "1s")
	v.SetDefault("report.pipeline.deadLetterCap", 128)
	v.SetDefault("report.filter.local.include", []string{})
	v.SetDefault("report.filter.local.exclude", []string{})
	v.SetDefault("report.filter.remote.collection", DefaultRemoteConfigCollection)
	v.SetDefault("report.filter.remote.documentID", DefaultRemoteConfigDocumentID)
	v.SetDefault("report.filter.remote.syncInterval", DefaultRemoteConfigInterval)
}
