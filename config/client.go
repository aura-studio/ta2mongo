package config

import "time"

// ClientConfig is the runtime configuration the client SDK / gateway / operator
// commands consume. It is the projection target of the unified RoleConfig
// (see RoleConfig.Client in role.go): its functionality is partitioned into the
// upload/backfill/sql/publish sections plus an HTTP server block for the
// gateway face. instanceID is NOT a client concept — it belongs to the worker.
type ClientConfig struct {
	Logging LoggingConfig `mapstructure:"logging"`
	Mongo   MongoConfig   `mapstructure:"mongo"`

	// StringUpload: single string ingest, no retransmission (function #1).
	StringUpload StringUploadConfig `mapstructure:"stringUpload"`
	// FileUpload: file ingest with resume/retransmission (function #2).
	FileUpload FileUploadConfig `mapstructure:"fileUpload"`
	// Backfill + BackfillFilter: historical backfill execution (function #3).
	Backfill       BackfillConfig       `mapstructure:"backfill"`
	BackfillFilter BackfillFilterConfig `mapstructure:"backfillFilter"`
	// SQL: ad-hoc SQL execution (function #4). Connection credentials come from
	// the backfill section; this only carries execution-specific overrides.
	SQL SQLConfig `mapstructure:"sql"`
	// Publish: MongoDB task-publishing — the publish end of the agent task
	// mechanism (function #5).
	Publish PublishConfig `mapstructure:"publish"`
	// Server configures the HTTP/REST face (`tango serve`).
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

// SQLConfig carries execution-specific overrides for ad-hoc SQL tasks.
type SQLConfig struct {
	SchemaPrefix string `mapstructure:"schemaPrefix"`
}

// PublishConfig configures the task-publishing client.
type PublishConfig struct {
	TasksCollection     string        `mapstructure:"tasksCollection"`
	InstancesCollection string        `mapstructure:"instancesCollection"`
	InstanceTTL         time.Duration `mapstructure:"instanceTTL"`
}

// ServerConfig configures the HTTP/REST face.
type ServerConfig struct {
	Addr string `mapstructure:"addr"`
}

// DefaultFileUploadCheckpointCollection is where file-upload resume offsets are
// stored when not overridden.
const DefaultFileUploadCheckpointCollection = "_tango_fileupload"

// DefaultServerAddr is the default HTTP listen address for `tango serve`.
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
	if c.Publish.TasksCollection == "" {
		c.Publish.TasksCollection = DefaultTasksCollection
	}
	if c.Publish.InstancesCollection == "" {
		c.Publish.InstancesCollection = DefaultInstancesCollection
	}
	if c.Publish.InstanceTTL <= 0 {
		c.Publish.InstanceTTL = DefaultInstanceTTL
	}
	if c.Server.Addr == "" {
		c.Server.Addr = DefaultServerAddr
	}
	applyBackfillFilterDefaults(&c.BackfillFilter)
}

// runtimeBase returns a runtime Config seeded with the shared logging + mongo
// settings, ready for a section-specific overlay.
func (c ClientConfig) runtimeBase() Config {
	rt := Config{Logging: c.Logging, Mongo: c.Mongo}
	return rt
}

// BackfillRuntime builds the runtime Config for a backfill execution.
func (c ClientConfig) BackfillRuntime() Config {
	rt := c.runtimeBase()
	rt.Mode = ModeBackfill
	rt.Backfill = c.Backfill
	rt.BackfillFilter = c.BackfillFilter
	applyDefaults(&rt)
	return rt
}

// SQLRuntime builds the runtime Config for ad-hoc SQL execution, applying the
// SQL section's schemaPrefix override on top of the backfill connection block.
func (c ClientConfig) SQLRuntime() Config {
	rt := c.BackfillRuntime()
	if c.SQL.SchemaPrefix != "" {
		rt.Backfill.SchemaPrefix = c.SQL.SchemaPrefix
	}
	return rt
}
