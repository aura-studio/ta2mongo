package config

import (
	"time"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/engine"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process"
	"rocket-nano/tools/tango/internal/process/pipeline"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// applyDefaults fills in zero-value fields with sensible defaults, allocating
// any section pointer that is nil so callers can rely on every section being
// present after this runs.
func applyDefaults(c *Config) {
	if c.Engine == nil {
		c.Engine = &engine.Config{}
	}
	if c.Engine.Mode == "" {
		c.Engine.Mode = ModeReport
	}

	if c.Runtime == nil {
		c.Runtime = &RuntimeConfig{}
	}
	if c.Runtime.Logging == nil {
		c.Runtime.Logging = &logging.Config{}
	}
	if c.Runtime.Logging.Level == "" {
		c.Runtime.Logging.Level = "info"
	}
	if c.Runtime.Logging.Format == "" {
		c.Runtime.Logging.Format = "text"
	}

	if c.Dao == nil {
		c.Dao = &dao.Config{}
	}
	if c.Dao.Mongo == nil {
		c.Dao.Mongo = &mongo.Config{}
	}
	if c.Dao.Mongo.ConnectTimeout <= 0 {
		c.Dao.Mongo.ConnectTimeout = 10 * time.Second
	}
	if c.Dao.Mongo.ServerSelectionTimeout <= 0 {
		c.Dao.Mongo.ServerSelectionTimeout = 30 * time.Second
	}
	if c.Dao.Store == nil {
		c.Dao.Store = &store.Config{}
	}
	if c.Dao.Store.MaxElapsedTime <= 0 {
		c.Dao.Store.MaxElapsedTime = 10 * time.Second
	}

	if c.Source == nil {
		c.Source = &tailer.Config{}
	}
	if c.Source.RescanInterval <= 0 {
		c.Source.RescanInterval = 30 * time.Second
	}
	if c.Source.TailMode == "" {
		c.Source.TailMode = tailer.TailModeHybrid
	}
	if c.Source.PollInterval <= 0 {
		c.Source.PollInterval = 200 * time.Millisecond
	}
	if c.Source.MaxLineBytes <= 0 {
		c.Source.MaxLineBytes = 10 * 1024 * 1024
	}

	if c.Process == nil {
		c.Process = &process.Config{}
	}
	if c.Process.Pipeline == nil {
		c.Process.Pipeline = &pipeline.Config{}
	}
	p := c.Process.Pipeline
	if p.BatchSize <= 0 {
		p.BatchSize = 1000
	}
	// BatchSizeMin/BatchSizeMax: 0 means auto-derive (handled by the pipeline
	// Config's MinBatchSize/MaxBatchSize methods). Clamp explicit values to a
	// valid range here for consistency.
	if p.BatchSizeMin > 0 && p.BatchSizeMin > p.BatchSize {
		p.BatchSizeMin = p.BatchSize
	}
	if p.BatchSizeMax > 0 && p.BatchSizeMax < p.BatchSize {
		p.BatchSizeMax = p.BatchSize
	}
	if p.BatchWorkers <= 0 {
		p.BatchWorkers = 2
	}
	if p.FlushInterval <= 0 {
		p.FlushInterval = time.Second
	}
	if p.DeadLetterCap <= 0 {
		p.DeadLetterCap = 128
	}

	if c.Parser == nil {
		c.Parser = &parser.Config{}
	}
	if c.Parser.Filter == nil {
		c.Parser.Filter = &filter.Config{}
	}
}
