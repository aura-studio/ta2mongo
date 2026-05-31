package config

import (
	"fmt"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// DaemonConfig is the schema of the daemon config file (daemon.yaml /
// daemon.json). The daemon performs the *reporting* role — tail logs → parse →
// filter → write to Mongo — and optionally hosts the agent (a feature toggled
// via agent.enabled). instanceID lives under agent because it is only
// meaningful when the agent is running.
type DaemonConfig struct {
	Logging      LoggingConfig     `mapstructure:"logging"`
	Mongo        MongoConfig       `mapstructure:"mongo"`
	Source       SourceConfig      `mapstructure:"source"`
	Pipeline     PipelineConfig    `mapstructure:"pipeline"`
	ReportFilter FilterConfig      `mapstructure:"reportFilter"`
	RemoteConfig RemoteConfig      `mapstructure:"remoteConfig"`
	Agent        DaemonAgentConfig `mapstructure:"agent"`
}

// DaemonAgentConfig is the agent feature block within the daemon config.
type DaemonAgentConfig struct {
	// Enabled turns the in-daemon agent on: register a heartbeat, claim tasks
	// from the shared queue, and execute them. When false the daemon only
	// reports.
	Enabled bool `mapstructure:"enabled"`
	// InstanceID uniquely identifies this agent within the database namespace.
	// Required when Enabled. This is the only place instanceID is configured.
	InstanceID string `mapstructure:"instanceID"`

	TasksCollection     string        `mapstructure:"tasksCollection"`
	InstancesCollection string        `mapstructure:"instancesCollection"`
	PollInterval        time.Duration `mapstructure:"pollInterval"`
	LeaseDuration       time.Duration `mapstructure:"leaseDuration"`
	HeartbeatInterval   time.Duration `mapstructure:"heartbeatInterval"`
	InstanceTTL         time.Duration `mapstructure:"instanceTTL"`
}

// ToRuntime projects the file-facing daemon config onto the internal runtime
// Config consumed by the daemon/agent packages.
func (d DaemonConfig) ToRuntime() Config {
	return Config{
		Mode:         ModeDaemon,
		InstanceID:   d.Agent.InstanceID,
		Logging:      d.Logging,
		Mongo:        d.Mongo,
		Source:       d.Source,
		Pipeline:     d.Pipeline,
		Filter:       d.ReportFilter,
		RemoteConfig: d.RemoteConfig,
		Agent: AgentConfig{
			Enabled:             d.Agent.Enabled,
			TasksCollection:     d.Agent.TasksCollection,
			InstancesCollection: d.Agent.InstancesCollection,
			PollInterval:        d.Agent.PollInterval,
			LeaseDuration:       d.Agent.LeaseDuration,
			HeartbeatInterval:   d.Agent.HeartbeatInterval,
			InstanceTTL:         d.Agent.InstanceTTL,
		},
	}
}

// LoadDaemon loads and validates a daemon config from path (+ env + flags),
// returning both the file-facing config and the projected runtime Config with
// defaults applied. An empty/absent path falls back to defaults + env + flags.
func LoadDaemon(path string, flags *pflag.FlagSet) (DaemonConfig, Config, error) {
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
	if dc.Agent.Enabled && dc.Agent.InstanceID == "" {
		return DaemonConfig{}, Config{}, fmt.Errorf("config: agent.instanceID is required when agent.enabled is true")
	}
	return dc, rt, nil
}

// setDaemonDefaults registers viper defaults for the daemon schema so that
// AutomaticEnv binding works for every key and unset values are sane.
func setDaemonDefaults(v *viper.Viper) {
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "text")
	v.SetDefault("mongo.uri", "")
	v.SetDefault("mongo.maxElapsedTime", "10s")
	v.SetDefault("mongo.connectTimeout", "10s")
	v.SetDefault("mongo.serverSelectionTimeout", "30s")
	v.SetDefault("source.logPattern", []string{})
	v.SetDefault("source.tailMode", TailModeHybrid)
	v.SetDefault("source.rescanInterval", "30s")
	v.SetDefault("source.pollInterval", "200ms")
	v.SetDefault("source.maxLineBytes", 10*1024*1024)
	v.SetDefault("pipeline.batchSize", 1000)
	v.SetDefault("pipeline.batchWorkers", 2)
	v.SetDefault("pipeline.flushInterval", "1s")
	v.SetDefault("pipeline.deadLetterCap", 128)
	v.SetDefault("reportFilter.include", []string{})
	v.SetDefault("reportFilter.exclude", []string{})
	v.SetDefault("remoteConfig.enabled", false)
	v.SetDefault("agent.enabled", false)
	v.SetDefault("agent.instanceID", "")
}

// daemonFlagKeys maps the daemon binary's CLI flags to nested config keys.
var daemonFlagKeys = FlagKeyMap{
	"mongoURI":   "mongo.uri",
	"logLevel":   "logging.level",
	"instanceID": "agent.instanceID",
}
