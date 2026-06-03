package config

import "time"

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(c *Config) {
	if c.Mode == "" {
		c.Mode = ModeReport
	}
	if c.Logging.Level == "" {
		c.Logging.Level = "info"
	}
	if c.Logging.Format == "" {
		c.Logging.Format = "text"
	}
	if c.Mongo.MaxElapsedTime <= 0 {
		c.Mongo.MaxElapsedTime = 10 * time.Second
	}
	if c.Mongo.ConnectTimeout <= 0 {
		c.Mongo.ConnectTimeout = 10 * time.Second
	}
	if c.Mongo.ServerSelectionTimeout <= 0 {
		c.Mongo.ServerSelectionTimeout = 30 * time.Second
	}
	if c.Source.RescanInterval <= 0 {
		c.Source.RescanInterval = 30 * time.Second
	}
	if c.Source.TailMode == "" {
		c.Source.TailMode = TailModeHybrid
	}
	if c.Source.PollInterval <= 0 {
		c.Source.PollInterval = 200 * time.Millisecond
	}
	if c.Source.MaxLineBytes <= 0 {
		c.Source.MaxLineBytes = 10 * 1024 * 1024
	}
	if c.Pipeline.BatchSize <= 0 {
		c.Pipeline.BatchSize = 1000
	}
	// BatchSizeMin/BatchSizeMax: 0 means auto-derive (handled by BatchSizeMin/Max methods).
	// Clamp explicit values to valid range here for consistency.
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
	applyBackfillDefaults(&c.Backfill)
	applyBackfillFilterDefaults(&c.BackfillFilter)
	applyRemoteConfigDefaults(&c.RemoteConfig)
	applyWorkerDefaults(&c.Worker)
}

func applyWorkerDefaults(a *WorkerConfig) {
	if a.TasksCollection == "" {
		a.TasksCollection = DefaultTasksCollection
	}
	if a.InstancesCollection == "" {
		a.InstancesCollection = DefaultInstancesCollection
	}
	if a.PollInterval <= 0 {
		a.PollInterval = DefaultWorkerPollInterval
	}
	if a.LeaseDuration <= 0 {
		a.LeaseDuration = DefaultWorkerLeaseDuration
	}
	if a.HeartbeatInterval <= 0 {
		a.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if a.InstanceTTL <= 0 {
		a.InstanceTTL = DefaultInstanceTTL
	}
}

func applyRemoteConfigDefaults(rc *RemoteConfig) {
	if rc.Collection == "" {
		rc.Collection = DefaultRemoteConfigCollection
	}
	if rc.DocumentID == "" {
		rc.DocumentID = DefaultRemoteConfigDocumentID
	}
	if rc.SyncInterval <= 0 {
		rc.SyncInterval = DefaultRemoteConfigInterval
	}
}

func applyBackfillFilterDefaults(b *BackfillFilterConfig) {
	if b.Table == "" {
		b.Table = BackfillTableEvent
	}
}

func applyBackfillDefaults(b *BackfillConfig) {
	if b.PageSize <= 0 {
		b.PageSize = 10000
	}
	if b.PageRetries <= 0 {
		b.PageRetries = 3
	}
	if b.PollInterval <= 0 {
		b.PollInterval = 3 * time.Second
	}
	if b.PollTimeout <= 0 {
		b.PollTimeout = 30 * time.Minute
	}
	if b.ProgressCollection == "" {
		b.ProgressCollection = DefaultProgressCollection
	}
}
