package config

import (
	"fmt"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// LoadReport loads the report-service config. It reuses the daemon file schema
// (generic + report) but does not enable the task-worker settings.
func LoadReport(path string, flags *pflag.FlagSet) (DaemonConfig, Config, error) {
	v := newViper()
	setDaemonDefaults(v)
	if err := readConfigFile(v, path); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	if err := bindFlagsTo(v, flags); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	applyRoleAliases(v)

	var dc DaemonConfig
	if err := v.Unmarshal(&dc, durationDecodeHook()); err != nil {
		return DaemonConfig{}, Config{}, fmt.Errorf("unmarshal report config: %w", err)
	}
	rt := dc.ToRuntime()
	applyDefaults(&rt)
	rt.Agent.Enabled = false

	if err := rt.Validate(); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	if len(dc.Report.Source.LogPattern) == 0 {
		return DaemonConfig{}, Config{}, fmt.Errorf("config: report.source.logPattern is required (at least one regex)")
	}
	return dc, rt, nil
}

// LoadWorker loads the task-worker service config. It reuses the daemon schema's
// generic + agent + report.filter.remote blocks, but it does not require a
// report source because no file-tailing pipeline is started.
func LoadWorker(path string, flags *pflag.FlagSet) (DaemonConfig, Config, error) {
	v := newViper()
	setDaemonDefaults(v)
	if err := readConfigFile(v, path); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	if err := bindFlagsTo(v, flags); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	applyRoleAliases(v)

	var dc DaemonConfig
	if err := v.Unmarshal(&dc, durationDecodeHook()); err != nil {
		return DaemonConfig{}, Config{}, fmt.Errorf("unmarshal worker config: %w", err)
	}
	rt := dc.ToRuntime()
	applyDefaults(&rt)
	rt.Mode = ModeAgent
	rt.RemoteConfig.Enabled = true
	rt.Agent.Enabled = true

	if dc.Agent.InstanceID == "" {
		return DaemonConfig{}, Config{}, fmt.Errorf("config: agent.instanceID is required for worker service")
	}
	if err := rt.Validate(); err != nil {
		return DaemonConfig{}, Config{}, err
	}
	return dc, rt, nil
}

func applyRoleAliases(v *viper.Viper) {
	copyIfSet(v, "logging.level", "generic.logging.level")
	copyIfSet(v, "logging.format", "generic.logging.format")
	copyIfSet(v, "mongo.uri", "generic.mongo.uri")
	copyIfSet(v, "mongo.maxElapsedTime", "generic.mongo.maxElapsedTime")
	copyIfSet(v, "mongo.connectTimeout", "generic.mongo.connectTimeout")
	copyIfSet(v, "mongo.serverSelectionTimeout", "generic.mongo.serverSelectionTimeout")
	copyIfSet(v, "instanceID", "agent.instanceID")
	copyIfSet(v, "tasks.collection", "agent.tasksCollection")
	copyIfSet(v, "tasks.instancesCollection", "agent.instancesCollection")
	copyIfSet(v, "tasks.pollInterval", "agent.pollInterval")
	copyIfSet(v, "tasks.leaseDuration", "agent.leaseDuration")
	copyIfSet(v, "tasks.heartbeatInterval", "agent.heartbeatInterval")
	copyIfSet(v, "tasks.instanceTTL", "agent.instanceTTL")
	copyIfSet(v, "remote-config.enabled", "report.filter.remote.enabled")
	copyIfSet(v, "remote-config.collection", "report.filter.remote.collection")
	copyIfSet(v, "remote-config.documentID", "report.filter.remote.documentID")
	copyIfSet(v, "remote-config.syncInterval", "report.filter.remote.syncInterval")
}

func copyIfSet(v *viper.Viper, from, to string) {
	if v.IsSet(from) {
		v.Set(to, v.Get(from))
	}
}
