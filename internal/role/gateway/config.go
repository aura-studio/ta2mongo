// Package gateway implements the HTTP gateway role: an HTTP server that wraps
// each request body as an httpbody source and ingests it through the embedded
// api engine (single / batch / pipeline). Its file config lives under the
// package-path key role.gateway.
package gateway

import (
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/process/pipeline"
)

// DefaultServerAddr is the default HTTP listen address for `tango gateway`.
const DefaultServerAddr = ":8080"

// DefaultUploadBatchSize is the bulk-write flush size used for single/batch
// uploads when none is configured.
const DefaultUploadBatchSize = 1000

// Config is the gateway role's own configuration (file key role.gateway). The
// shared MongoDB/logging settings live at the top level (dao.*, logging.*) and
// are injected by the command, not duplicated here.
type Config struct {
	// Addr is the HTTP listen address (file key role.gateway.addr).
	Addr string `mapstructure:"addr"`
	// Upload configures how request bodies are ingested.
	Upload UploadConfig `mapstructure:"upload"`
}

// UploadConfig configures HTTP-body uploads. Every upload wraps the request
// body as an httpbody source and runs it through the selected process strategy.
type UploadConfig struct {
	// DefaultMode selects the strategy when a request omits "mode":
	// single | batch | pipeline. Default batch.
	DefaultMode string `mapstructure:"defaultMode"`
	// BatchSize is the bulk-write flush size for single/batch modes.
	BatchSize int `mapstructure:"batchSize"`
	// Pipeline configures the async worker pool used by pipeline mode.
	Pipeline *pipeline.Config `mapstructure:"pipeline"`
	// Filter is the optional reporting filter applied to every uploaded line.
	Filter *filter.Config `mapstructure:"filter"`
}

// ProcessConfig projects the upload settings onto the process.Config consumed by
// the api engine.
func (c UploadConfig) ProcessConfig() *process.Config {
	return &process.Config{BatchSize: c.BatchSize, Pipeline: c.Pipeline}
}

// ApplyDefaults fills unset options with sensible defaults.
func (c *Config) ApplyDefaults() {
	if c.Addr == "" {
		c.Addr = DefaultServerAddr
	}
	c.Upload.ApplyDefaults()
}

// ApplyDefaults fills unset upload options.
func (c *UploadConfig) ApplyDefaults() {
	if c.DefaultMode == "" {
		c.DefaultMode = string(process.ModeBatch)
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultUploadBatchSize
	}
	if c.Pipeline == nil {
		c.Pipeline = &pipeline.Config{}
	}
	c.Pipeline.ApplyDefaults()
}
