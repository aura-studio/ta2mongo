// Package gateway implements the HTTP gateway role: a long-running HTTP
// server exposing ingest and upload APIs.
package gateway

import (
	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process/pipeline"
	sdk "rocket-nano/tools/tango/internal/role/gateway/client"
)

// GatewayRuntimeConfig is the runtime configuration the gateway role consumes.
// It is the projection target of the unified RoleConfig (see
// config.RoleConfig.GatewayRuntime in the config package): its functionality is
// the upload sections plus an HTTP server block for the gateway face.
type GatewayRuntimeConfig struct {
	Dao     *dao.Config     `mapstructure:"dao"`
	Logging *logging.Config `mapstructure:"logging"`

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

// DefaultServerAddr is the default HTTP listen address for `tango gateway`.
const DefaultServerAddr = ":8080"

// ApplyDefaults fills unset options with sensible defaults.
func (c *GatewayRuntimeConfig) ApplyDefaults() {
	if c.Dao == nil {
		c.Dao = &dao.Config{}
	}
	c.Dao.ApplyDefaults()
	if c.Logging == nil {
		c.Logging = &logging.Config{}
	}
	c.Logging.ApplyDefaults()
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
		c.CheckpointCollection = sdk.DefaultFileUploadCheckpointCollection
	}
}

// ApplyDefaults fills unset server options.
func (c *GatewayServerConfig) ApplyDefaults() {
	if c.Addr == "" {
		c.Addr = DefaultServerAddr
	}
}

// ParserConfigFromFilter builds a parser config from a filter config.
func ParserConfigFromFilter(fc *filter.Config) *parser.Config {
	return &parser.Config{Filter: fc}
}
