package config

import "rocket-nano/tools/tango/internal/dao/mongo"

// ClientConfig is the runtime configuration the client SDK / gateway service
// consume. It is the projection target of the unified RoleConfig (see
// RoleConfig.Client in role.go): its functionality is the upload sections plus
// an HTTP server block for the gateway face.
type ClientConfig struct {
	Logging LoggingConfig `mapstructure:"logging"`
	Mongo   mongo.Config  `mapstructure:"mongo"`

	// StringUpload: single string ingest, no retransmission.
	StringUpload StringUploadConfig `mapstructure:"stringUpload"`
	// FileUpload: file ingest with resume/retransmission.
	FileUpload FileUploadConfig `mapstructure:"fileUpload"`
	// Server configures the HTTP/REST face (`tango gateway`).
	Server ServerConfig `mapstructure:"server"`
}

// StringUploadConfig configures single-string ingest (no retransmission).
type StringUploadConfig struct {
	BatchSize int          `mapstructure:"batchSize"`
	Filter    FilterConfig `mapstructure:"filter"`
}

// FileUploadConfig configures file ingest with resume (retransmission).
type FileUploadConfig struct {
	LogPattern   []string       `mapstructure:"logPattern"`
	MaxLineBytes int            `mapstructure:"maxLineBytes"`
	Pipeline     PipelineConfig `mapstructure:"pipeline"`
	Filter       FilterConfig   `mapstructure:"filter"`
	// CheckpointCollection stores per-file resume offsets so an interrupted
	// upload restarts where it left off instead of re-reading from the top.
	CheckpointCollection string `mapstructure:"checkpointCollection"`
}

// ServerConfig configures the HTTP/REST face.
type ServerConfig struct {
	Addr string `mapstructure:"addr"`
}

// DefaultFileUploadCheckpointCollection is where file-upload resume offsets are
// stored when not overridden.
const DefaultFileUploadCheckpointCollection = "_tango_fileupload"

// DefaultServerAddr is the default HTTP listen address for `tango gateway`.
const DefaultServerAddr = ":8080"

func (c *ClientConfig) applyDefaults() {
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "text"
	}
	if c.StringUpload.BatchSize <= 0 {
		c.StringUpload.BatchSize = 1000
	}
	if c.FileUpload.MaxLineBytes <= 0 {
		c.FileUpload.MaxLineBytes = 10 * 1024 * 1024
	}
	if c.FileUpload.CheckpointCollection == "" {
		c.FileUpload.CheckpointCollection = DefaultFileUploadCheckpointCollection
	}
	if c.Server.Addr == "" {
		c.Server.Addr = DefaultServerAddr
	}
}
