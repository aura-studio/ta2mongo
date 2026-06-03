package config

import (
	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process/pipeline"
)

// GatewayRuntimeConfig is the runtime configuration the gateway role consumes.
// It is the projection target of the unified RoleConfig (see
// RoleConfig.GatewayRuntime in role.go): its functionality is the upload
// sections plus an HTTP server block for the gateway face. Each section is a
// pointer to the owning module's config struct.
type GatewayRuntimeConfig struct {
	Dao     *dao.Config    `mapstructure:"dao"`
	Runtime *RuntimeConfig `mapstructure:"runtime"`

	// StringUpload: single string ingest, no retransmission.
	StringUpload StringUploadConfig `mapstructure:"stringUpload"`
	// FileUpload: file ingest with resume/retransmission.
	FileUpload FileUploadConfig `mapstructure:"fileUpload"`
	// Server configures the HTTP/REST face (`tango gateway`).
	Server GatewayServerConfig `mapstructure:"server"`
}

// StringUploadConfig configures single-string ingest (no retransmission).
type StringUploadConfig struct {
	BatchSize int            `mapstructure:"batchSize"`
	Filter    *filter.Config `mapstructure:"filter"`
}

// FileUploadConfig configures file ingest with resume/retransmission.
type FileUploadConfig struct {
	LogPattern   []string         `mapstructure:"logPattern"`
	MaxLineBytes int              `mapstructure:"maxLineBytes"`
	Pipeline     *pipeline.Config `mapstructure:"pipeline"`
	Filter       *filter.Config   `mapstructure:"filter"`
	// CheckpointCollection stores per-file resume offsets so an interrupted
	// upload restarts where it left off instead of re-reading from the top.
	CheckpointCollection string `mapstructure:"checkpointCollection"`
}

// GatewayServerConfig configures the HTTP/REST face.
type GatewayServerConfig struct {
	Addr string `mapstructure:"addr"`
}

// DefaultFileUploadCheckpointCollection is where file-upload resume offsets are
// stored when not overridden.
const DefaultFileUploadCheckpointCollection = "_tango_fileupload"

// DefaultServerAddr is the default HTTP listen address for `tango gateway`.
const DefaultServerAddr = ":8080"

func (c *GatewayRuntimeConfig) applyDefaults() {
	if c.Runtime == nil {
		c.Runtime = &RuntimeConfig{}
	}
	c.Runtime.ApplyDefaults()
	if c.Dao == nil {
		c.Dao = &dao.Config{}
	}
	c.Dao.ApplyDefaults()
	c.StringUpload.ApplyDefaults()
	c.FileUpload.ApplyDefaults()
	c.Server.ApplyDefaults()
}

// ApplyDefaults fills unset string-upload options.
func (c *StringUploadConfig) ApplyDefaults() {
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
}

// ApplyDefaults fills unset file-upload options.
func (c *FileUploadConfig) ApplyDefaults() {
	if c.Pipeline == nil {
		c.Pipeline = &pipeline.Config{}
	}
	c.Pipeline.ApplyDefaults()
	if c.MaxLineBytes <= 0 {
		c.MaxLineBytes = 10 * 1024 * 1024
	}
	if c.CheckpointCollection == "" {
		c.CheckpointCollection = DefaultFileUploadCheckpointCollection
	}
}

// ApplyDefaults fills unset server options.
func (c *GatewayServerConfig) ApplyDefaults() {
	if c.Addr == "" {
		c.Addr = DefaultServerAddr
	}
}

func parserConfigFromFilter(fc *filter.Config) *parser.Config {
	return &parser.Config{Filter: fc}
}
