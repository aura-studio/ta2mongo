package config

import (
	"time"

	"rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/log"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/process/pipeline"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// applyDefaults fills in zero-value fields with sensible defaults, allocating
// any section pointer that is nil so callers can rely on every section being
// present after this runs.
func applyDefaults(c *Config) {
	if c.Mode == "" {
		c.Mode = ModeReport
	}

	if c.Logging == nil {
		c.Logging = &log.Config{}
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "text"
	}

	if c.Mongo == nil {
		c.Mongo = &mongo.Config{}
	}
	if c.Mongo.ConnectTimeout <= 0 {
		c.Mongo.ConnectTimeout = 10 * time.Second
	}
	if c.Mongo.ServerSelectionTimeout <= 0 {
		c.Mongo.ServerSelectionTimeout = 30 * time.Second
	}

	if c.Store == nil {
		c.Store = &store.Config{}
	}
	if c.Store.MaxElapsedTime <= 0 {
		c.Store.MaxElapsedTime = 10 * time.Second
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

	if c.Pipeline == nil {
		c.Pipeline = &pipeline.Config{}
	}
	if c.Pipeline.BatchSize <= 0 {
		c.Pipeline.BatchSize = 1000
	}
	// BatchSizeMin/BatchSizeMax: 0 means auto-derive (handled by the pipeline
	// Config's MinBatchSize/MaxBatchSize methods). Clamp explicit values to a
	// valid range here for consistency.
	if c.Pipeline.BatchSizeMin > 0 && c.Pipeline.BatchSizeMin > c.Pipeline.BatchSize {
		c.Pipeline.BatchSizeMin = c.Pipeline.BatchSize
	}
	if c.Pipeline.BatchSizeMax > 0 && c.Pipeline.BatchSizeMax < c.Pipeline.BatchSize {
		c.Pipeline.BatchSizeMax = c.Pipeline.BatchSize
	}
	if c.Pipeline.BatchWorkers <= 0 {
		c.Pipeline.BatchWorkers = 2
	}
	if c.Pipeline.FlushInterval <= 0 {
		c.Pipeline.FlushInterval = time.Second
	}
	if c.Pipeline.DeadLetterCap <= 0 {
		c.Pipeline.DeadLetterCap = 128
	}

	if c.Filter == nil {
		c.Filter = &filter.Config{}
	}
}
