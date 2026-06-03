package config

import (
	"fmt"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/log"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process/pipeline"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// RoleConfig is the unified, role-oriented config file schema. One schema backs
// every role-named config file — standalone.yaml, gateway.yaml — and each role
// loader projects the sections it needs onto the internal runtime Config /
// ClientConfig. Sections:
//
//   - runtime: process-wide logging + MongoDB connection + store (every role).
//   - report:  the reporting pipeline (standalone service).
//   - gateway: the HTTP gateway listen address (gateway service).
//   - upload:  string + file upload settings (gateway + SDK).
//
// It is the only file-facing schema; standalone projects onto the runtime
// Config, gateway onto ClientConfig (client.go).
type RoleConfig struct {
	Runtime RuntimeConfig    `mapstructure:"runtime"`
	Report  RoleReportConfig `mapstructure:"report"`
	Gateway GatewayConfig    `mapstructure:"gateway"`
	Upload  UploadConfig     `mapstructure:"upload"`
}

// RuntimeConfig is the process-wide block shared by every role.
type RuntimeConfig struct {
	Logging *log.Config   `mapstructure:"logging"`
	Mongo   *mongo.Config `mapstructure:"mongo"`
	Store   *store.Config `mapstructure:"store"`
}

func (r RuntimeConfig) Dao() *dao.Config {
	return &dao.Config{Mongo: r.Mongo, Store: r.Store}
}

// RoleReportConfig is the reporting pipeline block for the standalone service.
type RoleReportConfig struct {
	Source   *tailer.Config   `mapstructure:"source"`
	Pipeline *pipeline.Config `mapstructure:"pipeline"`
	Filter   *filter.Config   `mapstructure:"filter"`
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
	BatchSize int            `mapstructure:"batchSize"`
	Filter    *filter.Config `mapstructure:"filter"`
}

// UploadFileConfig configures file ingest with resume (retransmission).
type UploadFileConfig struct {
	LogPattern           []string         `mapstructure:"logPattern"`
	MaxLineBytes         int              `mapstructure:"maxLineBytes"`
	Pipeline             *pipeline.Config `mapstructure:"pipeline"`
	Filter               *filter.Config   `mapstructure:"filter"`
	CheckpointCollection string           `mapstructure:"checkpointCollection"`
}

// ReportRuntime projects the unified config onto the runtime Config consumed by
// the standalone report service.
func (r RoleConfig) ReportRuntime() Config {
	return Config{
		Mode:     ModeReport,
		Logging:  r.Runtime.Logging,
		Dao:      r.Runtime.Dao(),
		Source:   r.Report.Source,
		Pipeline: r.Report.Pipeline,
		Parser:   parserConfigFromFilter(r.Report.Filter),
	}
}

// Client projects the unified config onto the ClientConfig consumed by the
// gateway service (which drives the client SDK).
func (r RoleConfig) Client() ClientConfig {
	cc := ClientConfig{
		Logging: r.Runtime.Logging,
		Dao:     r.Runtime.Dao(),
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

	var rc RoleConfig
	if err := v.Unmarshal(&rc, durationDecodeHook(), weaklyTyped()); err != nil {
		return RoleConfig{}, fmt.Errorf("unmarshal role config: %w", err)
	}
	return rc, nil
}

// LoadStandalone loads the unified standalone-service config (runtime + report)
// from a standalone.yaml-style file (+ env + flags).
func LoadStandalone(path string, flags *pflag.FlagSet) (RoleConfig, Config, error) {
	rc, err := loadRole(path, flags)
	if err != nil {
		return RoleConfig{}, Config{}, err
	}
	rt := rc.ReportRuntime()
	applyDefaults(&rt)

	if err := rt.Validate(); err != nil {
		return RoleConfig{}, Config{}, err
	}
	if rc.Report.Source == nil || len(rc.Report.Source.LogPattern) == 0 {
		return RoleConfig{}, Config{}, fmt.Errorf("config: report.source.logPattern is required (at least one regex)")
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

// setRoleDefaults registers viper defaults for every unified key so AutomaticEnv
// binding works and unset values are sane. Post-unmarshal, the projected
// Config/ClientConfig run their own applyDefaults for final normalisation.
func setRoleDefaults(v *viper.Viper) {
	// runtime
	v.SetDefault("runtime.logging.level", "info")
	v.SetDefault("runtime.logging.format", "text")
	v.SetDefault("runtime.mongo.uri", "")
	v.SetDefault("runtime.mongo.connectTimeout", "10s")
	v.SetDefault("runtime.mongo.serverSelectionTimeout", "30s")
	v.SetDefault("runtime.store.maxElapsedTime", "10s")
	// report
	v.SetDefault("report.source.logPattern", []string{})
	v.SetDefault("report.source.tailMode", tailer.TailModeHybrid)
	v.SetDefault("report.source.rescanInterval", "30s")
	v.SetDefault("report.source.pollInterval", "200ms")
	v.SetDefault("report.source.maxLineBytes", 10*1024*1024)
	v.SetDefault("report.pipeline.batchSize", 1000)
	v.SetDefault("report.pipeline.batchSizeMin", 0)
	v.SetDefault("report.pipeline.batchSizeMax", 0)
	v.SetDefault("report.pipeline.batchWorkers", 2)
	v.SetDefault("report.pipeline.flushInterval", "1s")
	v.SetDefault("report.pipeline.channelBuffer", 0)
	v.SetDefault("report.pipeline.deadLetterCap", 128)
	v.SetDefault("report.filter.include", []string{})
	v.SetDefault("report.filter.exclude", []string{})
	// gateway
	v.SetDefault("gateway.addr", DefaultServerAddr)
	// upload
	v.SetDefault("upload.string.batchSize", 1000)
	v.SetDefault("upload.file.logPattern", []string{})
	v.SetDefault("upload.file.maxLineBytes", 10*1024*1024)
	v.SetDefault("upload.file.checkpointCollection", DefaultFileUploadCheckpointCollection)
}
