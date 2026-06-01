package config

import (
	"fmt"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Daemon run modes, selected by the daemon subcommand
// (`tango daemon standalone` / `tango daemon agent`).
const (
	// DaemonModeStandalone is the self-contained reporting daemon: tail logs →
	// parse → report filter → write to Mongo. The filter is local; it does NOT
	// pull remote config and does NOT claim tasks.
	DaemonModeStandalone = "standalone"
	// DaemonModeAgent is the cluster worker daemon: it does everything
	// standalone does (report) and additionally syncs the report filter from the
	// remote-config document and registers as a task agent — claiming and
	// executing published tasks (report-sync / backfill / sql).
	DaemonModeAgent = "agent"
)

// DaemonConfig is the schema of the daemon config file (daemon.yaml /
// daemon.json). It is organised into three parts:
//
//   - common: process-wide settings shared by both modes (logging + the
//     MongoDB connection).
//   - report: the reporting pipeline — tail logs → parse → report filter →
//     write to Mongo. Used by both modes. The remoteConfig sub-block (consulted
//     only in agent mode) hot-reloads the filter from a control plane.
//   - agent:  task-agent settings (heartbeat + claim/execute). Used only in
//     agent mode. instanceID lives here because it is only meaningful then.
//
// There is no enabled switch: the active mode is chosen by the subcommand, not
// the file.
type DaemonConfig struct {
	Common CommonConfig       `mapstructure:"common"`
	Report DaemonReportConfig `mapstructure:"report"`
	Agent  DaemonAgentConfig  `mapstructure:"agent"`
}

// CommonConfig holds the process-wide settings shared by both modes: log output
// and the MongoDB connection (both modes write through the same connection).
type CommonConfig struct {
	Logging LoggingConfig `mapstructure:"logging"`
	Mongo   MongoConfig   `mapstructure:"mongo"`
}

// DaemonReportConfig is the reporting pipeline block, used by both modes. The
// daemon tails logPattern files, applies the report filter, and writes to
// Mongo. remoteConfig is consulted only in agent mode (standalone keeps the
// local filter verbatim).
type DaemonReportConfig struct {
	Source       SourceConfig   `mapstructure:"source"`
	Pipeline     PipelineConfig `mapstructure:"pipeline"`
	Filter       FilterConfig   `mapstructure:"filter"`
	RemoteConfig RemoteConfig   `mapstructure:"remoteConfig"`
}

// DaemonAgentConfig is the task-agent block, used only in agent mode.
type DaemonAgentConfig struct {
	// InstanceID uniquely identifies this agent within the database namespace.
	// Required in agent mode. This is the only place instanceID is configured.
	InstanceID string `mapstructure:"instanceID"`

	TasksCollection     string        `mapstructure:"tasksCollection"`
	InstancesCollection string        `mapstructure:"instancesCollection"`
	PollInterval        time.Duration `mapstructure:"pollInterval"`
	LeaseDuration       time.Duration `mapstructure:"leaseDuration"`
	HeartbeatInterval   time.Duration `mapstructure:"heartbeatInterval"`
	InstanceTTL         time.Duration `mapstructure:"instanceTTL"`
}

// ToRuntime projects the file-facing daemon config onto the internal runtime
// Config consumed by the daemon/agent packages. The mode-derived switches
// (RemoteConfig.Enabled, Agent.Enabled) are applied by LoadDaemon, not here.
func (d DaemonConfig) ToRuntime() Config {
	return Config{
		Mode:         ModeDaemon,
		InstanceID:   d.Agent.InstanceID,
		Logging:      d.Common.Logging,
		Mongo:        d.Common.Mongo,
		Source:       d.Report.Source,
		Pipeline:     d.Report.Pipeline,
		Filter:       d.Report.Filter,
		RemoteConfig: d.Report.RemoteConfig,
		Agent: AgentConfig{
			TasksCollection:     d.Agent.TasksCollection,
			InstancesCollection: d.Agent.InstancesCollection,
			PollInterval:        d.Agent.PollInterval,
			LeaseDuration:       d.Agent.LeaseDuration,
			HeartbeatInterval:   d.Agent.HeartbeatInterval,
			InstanceTTL:         d.Agent.InstanceTTL,
		},
	}
}

// LoadDaemon loads and validates a daemon config for the given run mode
// (DaemonModeStandalone / DaemonModeAgent) from path (+ env + flags), returning
// both the file-facing config and the projected runtime Config with defaults
// and mode-derived switches applied. An empty/absent path falls back to
// defaults + env + flags.
func LoadDaemon(path string, flags *pflag.FlagSet, mode string) (DaemonConfig, Config, error) {
	switch mode {
	case DaemonModeStandalone, DaemonModeAgent:
		// valid
	default:
		return DaemonConfig{}, Config{}, fmt.Errorf("config: unknown daemon mode %q (want %q or %q)", mode, DaemonModeStandalone, DaemonModeAgent)
	}

	v := newViper()
	setDaemonDefaults(v)
	if err := readConfigFile(v, path); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	if err := bindFlagsTo(v, flags, daemonFlagKeys); err != nil {
		return DaemonConfig{}, Config{}, err
	}

	var dc DaemonConfig
	if err := v.Unmarshal(&dc, durationDecodeHook()); err != nil {
		return DaemonConfig{}, Config{}, fmt.Errorf("unmarshal daemon config: %w", err)
	}

	rt := dc.ToRuntime()
	applyDefaults(&rt)

	// Mode determines the control-plane switches, not the file.
	switch mode {
	case DaemonModeStandalone:
		rt.RemoteConfig.Enabled = false
		rt.Agent.Enabled = false
	case DaemonModeAgent:
		rt.RemoteConfig.Enabled = true
		rt.Agent.Enabled = true
	}

	// Mirror runtime-normalised agent settings back so callers reading dc see
	// the same effective values.
	dc.Agent.TasksCollection = rt.Agent.TasksCollection
	dc.Agent.InstancesCollection = rt.Agent.InstancesCollection
	dc.Agent.PollInterval = rt.Agent.PollInterval
	dc.Agent.LeaseDuration = rt.Agent.LeaseDuration
	dc.Agent.HeartbeatInterval = rt.Agent.HeartbeatInterval
	dc.Agent.InstanceTTL = rt.Agent.InstanceTTL

	if err := rt.Validate(); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	// Both modes report, so a log pattern is always required — fail fast at load
	// time rather than at pipeline start.
	if len(dc.Report.Source.LogPattern) == 0 {
		return DaemonConfig{}, Config{}, fmt.Errorf("config: report.source.logPattern is required (at least one regex)")
	}
	if mode == DaemonModeAgent && dc.Agent.InstanceID == "" {
		return DaemonConfig{}, Config{}, fmt.Errorf("config: agent.instanceID is required in agent mode")
	}
	return dc, rt, nil
}

// setDaemonDefaults registers viper defaults for the daemon schema so that
// AutomaticEnv binding works for every key and unset values are sane. Env vars
// follow the nested key with TANGO_ prefix and "." → "_" (e.g.
// common.mongo.uri → TANGO_COMMON_MONGO_URI, agent.instanceID →
// TANGO_AGENT_INSTANCEID).
func setDaemonDefaults(v *viper.Viper) {
	v.SetDefault("common.logging.level", "info")
	v.SetDefault("common.logging.format", "text")
	v.SetDefault("common.mongo.uri", "")
	v.SetDefault("common.mongo.maxElapsedTime", "10s")
	v.SetDefault("common.mongo.connectTimeout", "10s")
	v.SetDefault("common.mongo.serverSelectionTimeout", "30s")
	v.SetDefault("report.source.logPattern", []string{})
	v.SetDefault("report.source.tailMode", TailModeHybrid)
	v.SetDefault("report.source.rescanInterval", "30s")
	v.SetDefault("report.source.pollInterval", "200ms")
	v.SetDefault("report.source.maxLineBytes", 10*1024*1024)
	v.SetDefault("report.pipeline.batchSize", 1000)
	v.SetDefault("report.pipeline.batchWorkers", 2)
	v.SetDefault("report.pipeline.flushInterval", "1s")
	v.SetDefault("report.pipeline.deadLetterCap", 128)
	v.SetDefault("report.filter.include", []string{})
	v.SetDefault("report.filter.exclude", []string{})
	v.SetDefault("agent.instanceID", "")
}

// daemonFlagKeys maps the daemon binary's CLI flags to nested config keys.
var daemonFlagKeys = FlagKeyMap{
	"mongoURI":   "common.mongo.uri",
	"logLevel":   "common.logging.level",
	"instanceID": "agent.instanceID",
}
