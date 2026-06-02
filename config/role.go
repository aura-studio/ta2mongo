package config

import (
	"fmt"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// RoleConfig is the unified, role-oriented config file schema (Phase 4). One
// schema backs every role-named config file — report.yaml, worker.yaml,
// gateway.yaml, operator.yaml — and each role loader projects the sections it
// needs onto the internal runtime Config / ClientConfig. Sections:
//
//   - runtime:        process-wide logging + MongoDB connection (every role).
//   - report:         the reporting pipeline (report service).
//   - remoteConfig:   the MongoDB control-plane override (report + worker).
//   - tasks:          the task queue (worker + operator/gateway publish).
//   - gateway:        the HTTP gateway listen address (gateway service).
//   - upload:         string + file upload settings (operator + gateway + SDK).
//   - backfill /
//     backfillFilter / sql: backfill + ad-hoc SQL (worker + operator + gateway).
//
// The legacy daemon (generic/report/agent) and client schemas remain in
// daemon.go / client.go for the compatibility wrapper commands.
type RoleConfig struct {
	Runtime        RuntimeConfig        `mapstructure:"runtime"`
	Report         RoleReportConfig     `mapstructure:"report"`
	RemoteConfig   RemoteConfig         `mapstructure:"remoteConfig"`
	Tasks          TasksConfig          `mapstructure:"tasks"`
	Gateway        GatewayConfig        `mapstructure:"gateway"`
	Upload         UploadConfig         `mapstructure:"upload"`
	Backfill       BackfillConfig       `mapstructure:"backfill"`
	BackfillFilter BackfillFilterConfig `mapstructure:"backfillFilter"`
	SQL            SQLConfig            `mapstructure:"sql"`
}

// RuntimeConfig is the process-wide block shared by every role.
type RuntimeConfig struct {
	Logging LoggingConfig `mapstructure:"logging"`
	Mongo   MongoConfig   `mapstructure:"mongo"`
}

// RoleReportConfig is the reporting pipeline block for the report service.
type RoleReportConfig struct {
	Source   SourceConfig   `mapstructure:"source"`
	Pipeline PipelineConfig `mapstructure:"pipeline"`
	Filter   FilterConfig   `mapstructure:"filter"`
}

// TasksConfig is the task-queue block. instanceID identifies a worker instance
// (required for the worker service; unused by publishers).
type TasksConfig struct {
	InstanceID          string        `mapstructure:"instanceID"`
	Collection          string        `mapstructure:"collection"`
	InstancesCollection string        `mapstructure:"instancesCollection"`
	PollInterval        time.Duration `mapstructure:"pollInterval"`
	LeaseTTL            time.Duration `mapstructure:"leaseTTL"`
	HeartbeatInterval   time.Duration `mapstructure:"heartbeatInterval"`
	InstanceTTL         time.Duration `mapstructure:"instanceTTL"`
}

// GatewayConfig is the HTTP gateway block.
type GatewayConfig struct {
	Addr string `mapstructure:"addr"`
}

// UploadConfig groups the string + file upload settings.
type UploadConfig struct {
	String UploadStringConfig `mapstructure:"string"`
	File   UploadFileConfig   `mapstructure:"file"`
}

// UploadStringConfig configures single-string ingest (no retransmission).
type UploadStringConfig struct {
	BatchSize int          `mapstructure:"batchSize"`
	Filter    FilterConfig `mapstructure:"filter"`
}

// UploadFileConfig configures file ingest with resume (retransmission).
type UploadFileConfig struct {
	LogPattern           []string       `mapstructure:"logPattern"`
	MaxLineBytes         int            `mapstructure:"maxLineBytes"`
	Pipeline             PipelineConfig `mapstructure:"pipeline"`
	Filter               FilterConfig   `mapstructure:"filter"`
	CheckpointCollection string         `mapstructure:"checkpointCollection"`
}

// ReportRuntime projects the unified config onto the runtime Config consumed by
// the report service. The control-plane switches (RemoteConfig.Enabled,
// Agent.Enabled) are applied by LoadReport, not here.
func (r RoleConfig) ReportRuntime() Config {
	return Config{
		Mode:         ModeDaemon,
		Logging:      r.Runtime.Logging,
		Mongo:        r.Runtime.Mongo,
		Source:       r.Report.Source,
		Pipeline:     r.Report.Pipeline,
		Filter:       r.Report.Filter,
		RemoteConfig: r.RemoteConfig,
	}
}

// WorkerRuntime projects the unified config onto the runtime Config consumed by
// the worker service. The control-plane switches are applied by LoadWorker.
func (r RoleConfig) WorkerRuntime() Config {
	return Config{
		Mode:         ModeAgent,
		InstanceID:   r.Tasks.InstanceID,
		Logging:      r.Runtime.Logging,
		Mongo:        r.Runtime.Mongo,
		RemoteConfig: r.RemoteConfig,
		Agent: AgentConfig{
			TasksCollection:     r.Tasks.Collection,
			InstancesCollection: r.Tasks.InstancesCollection,
			PollInterval:        r.Tasks.PollInterval,
			LeaseDuration:       r.Tasks.LeaseTTL,
			HeartbeatInterval:   r.Tasks.HeartbeatInterval,
			InstanceTTL:         r.Tasks.InstanceTTL,
		},
	}
}

// Client projects the unified config onto the ClientConfig consumed by the
// operator commands and the gateway service (both drive the client SDK).
func (r RoleConfig) Client() ClientConfig {
	cc := ClientConfig{
		Logging: r.Runtime.Logging,
		Mongo:   r.Runtime.Mongo,
		StringUpload: StringUploadConfig{
			BatchSize: r.Upload.String.BatchSize,
			Filter:    r.Upload.String.Filter,
		},
		FileUpload: FileUploadConfig{
			LogPattern:           r.Upload.File.LogPattern,
			MaxLineBytes:         r.Upload.File.MaxLineBytes,
			Pipeline:             r.Upload.File.Pipeline,
			Filter:               r.Upload.File.Filter,
			CheckpointCollection: r.Upload.File.CheckpointCollection,
		},
		Backfill:       r.Backfill,
		BackfillFilter: r.BackfillFilter,
		SQL:            r.SQL,
		Publish: PublishConfig{
			TasksCollection:     r.Tasks.Collection,
			InstancesCollection: r.Tasks.InstancesCollection,
			InstanceTTL:         r.Tasks.InstanceTTL,
		},
		Server: ServerConfig{Addr: r.Gateway.Addr},
	}
	cc.applyDefaults()
	return cc
}

// loadRole reads the unified RoleConfig from path (+ env + flags). Env vars
// follow the nested key with the TANGO_ prefix and "." → "_"
// (e.g. runtime.mongo.uri → TANGO_RUNTIME_MONGO_URI).
func loadRole(path string, flags *pflag.FlagSet) (RoleConfig, error) {
	v := newViper()
	setRoleDefaults(v)
	if err := readConfigFile(v, path); err != nil {
		return RoleConfig{}, err
	}
	if err := bindFlagsTo(v, flags); err != nil {
		return RoleConfig{}, err
	}
	applyRoleAliases(v)

	var rc RoleConfig
	if err := v.Unmarshal(&rc, durationDecodeHook()); err != nil {
		return RoleConfig{}, fmt.Errorf("unmarshal role config: %w", err)
	}
	return rc, nil
}

// LoadReport loads the unified report-service config (runtime + report +
// remoteConfig) from a report.yaml-style file (+ env + flags).
func LoadReport(path string, flags *pflag.FlagSet) (RoleConfig, Config, error) {
	rc, err := loadRole(path, flags)
	if err != nil {
		return RoleConfig{}, Config{}, err
	}
	rt := rc.ReportRuntime()
	applyDefaults(&rt)
	rt.Agent.Enabled = false

	if err := rt.Validate(); err != nil {
		return RoleConfig{}, Config{}, err
	}
	if len(rc.Report.Source.LogPattern) == 0 {
		return RoleConfig{}, Config{}, fmt.Errorf("config: report.source.logPattern is required (at least one regex)")
	}
	return rc, rt, nil
}

// LoadWorker loads the unified task-worker config (runtime + tasks +
// remoteConfig). The worker requires tasks.instanceID and always runs with the
// task worker and remote-config control plane enabled.
func LoadWorker(path string, flags *pflag.FlagSet) (RoleConfig, Config, error) {
	rc, err := loadRole(path, flags)
	if err != nil {
		return RoleConfig{}, Config{}, err
	}
	rt := rc.WorkerRuntime()
	applyDefaults(&rt)
	rt.Mode = ModeAgent
	rt.RemoteConfig.Enabled = true
	rt.Agent.Enabled = true

	if rc.Tasks.InstanceID == "" {
		return RoleConfig{}, Config{}, fmt.Errorf("config: tasks.instanceID is required for the worker service")
	}
	if err := rt.Validate(); err != nil {
		return RoleConfig{}, Config{}, err
	}
	return rc, rt, nil
}

// LoadGateway loads the unified gateway-service config and projects it onto the
// ClientConfig the HTTP server runs on.
func LoadGateway(path string, flags *pflag.FlagSet) (RoleConfig, ClientConfig, error) {
	rc, err := loadRole(path, flags)
	if err != nil {
		return RoleConfig{}, ClientConfig{}, err
	}
	return rc, rc.Client(), nil
}

// LoadOperator loads the unified operator config and projects it onto the
// ClientConfig the one-shot commands run on.
func LoadOperator(path string, flags *pflag.FlagSet) (RoleConfig, ClientConfig, error) {
	rc, err := loadRole(path, flags)
	if err != nil {
		return RoleConfig{}, ClientConfig{}, err
	}
	return rc, rc.Client(), nil
}

// applyRoleAliases maps convenience flags onto their canonical unified keys so
// the flag name and config key stay aligned while a short alias still works.
func applyRoleAliases(v *viper.Viper) {
	copyIfSet(v, "instanceID", "tasks.instanceID")
}

func copyIfSet(v *viper.Viper, from, to string) {
	if v.IsSet(from) {
		v.Set(to, v.Get(from))
	}
}

// setRoleDefaults registers viper defaults for every unified key so AutomaticEnv
// binding works and unset values are sane. Post-unmarshal, the projected
// Config/ClientConfig run their own applyDefaults for final normalisation.
func setRoleDefaults(v *viper.Viper) {
	// runtime
	v.SetDefault("runtime.logging.level", "info")
	v.SetDefault("runtime.logging.format", "text")
	v.SetDefault("runtime.mongo.uri", "")
	v.SetDefault("runtime.mongo.maxElapsedTime", "10s")
	v.SetDefault("runtime.mongo.connectTimeout", "10s")
	v.SetDefault("runtime.mongo.serverSelectionTimeout", "30s")
	// report
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
	// remoteConfig
	v.SetDefault("remoteConfig.enabled", false)
	v.SetDefault("remoteConfig.collection", DefaultRemoteConfigCollection)
	v.SetDefault("remoteConfig.documentID", DefaultRemoteConfigDocumentID)
	v.SetDefault("remoteConfig.syncInterval", DefaultRemoteConfigInterval)
	// tasks
	v.SetDefault("tasks.instanceID", "")
	v.SetDefault("tasks.collection", DefaultTasksCollection)
	v.SetDefault("tasks.instancesCollection", DefaultInstancesCollection)
	v.SetDefault("tasks.pollInterval", DefaultAgentPollInterval)
	v.SetDefault("tasks.leaseTTL", DefaultAgentLeaseDuration)
	v.SetDefault("tasks.heartbeatInterval", DefaultHeartbeatInterval)
	v.SetDefault("tasks.instanceTTL", DefaultInstanceTTL)
	// gateway
	v.SetDefault("gateway.addr", DefaultServerAddr)
	// upload
	v.SetDefault("upload.string.batchSize", 1000)
	v.SetDefault("upload.file.logPattern", []string{})
	v.SetDefault("upload.file.maxLineBytes", 10*1024*1024)
	v.SetDefault("upload.file.checkpointCollection", DefaultFileUploadCheckpointCollection)
	// backfill selection
	v.SetDefault("backfillFilter.table", BackfillTableEvent)
}
