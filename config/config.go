// Package config defines tango's configuration.
//
// The single file-facing schema is RoleConfig (role.go): the unified
// report/worker/gateway/operator config file. Its loaders project it onto the
// shared runtime Config in this file (report/worker) and onto ClientConfig
// (client.go, the gateway/operator runtime projection). The internal service
// packages (report/worker/gateway/backfill) consume those. loader.go holds the
// shared YAML/JSON + TANGO_* env + flag loading helpers.
//
// Sources, in increasing priority: built-in defaults < config file (YAML or
// JSON, by extension) < TANGO_* environment variables < CLI flags. The
// remote-config document (MongoDB) overrides only the reporting filter and is
// applied separately at startup / via report-sync tasks.
//
// The runtime Load below loads the flat runtime Config directly; it is retained
// for tests and remote-config merging — production binaries use the role
// loaders (LoadReport / LoadWorker / LoadGateway / LoadOperator).
package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"rocket-nano/tools/tango/internal/core/filter"
)

// Mode constants identify the runtime role a Config drives. Mode is an internal
// runtime distinction consumed only by Validate (it is never persisted and
// cannot be set from the remote-config document); the role loaders set it from
// the command that built the Config.
const (
	// ModeReport is the report service runtime: tail logs -> filter -> Mongo.
	ModeReport = "report"
	// ModeWorker is the task worker runtime; it requires InstanceID.
	ModeWorker = "worker"
	// ModeBackfill is a backfill execution; it validates the backfill block.
	ModeBackfill = "backfill"
)

// BackfillTable constants name the TA virtual table being queried.
const (
	BackfillTableEvent = "event"
	BackfillTableUser  = "user"
)

// DefaultProgressCollection is the Mongo collection used for backfill
// checkpoints when the user does not override it.
const DefaultProgressCollection = "_backfill_progress"

// Remote-config defaults.
const (
	DefaultRemoteConfigCollection = "_tango_config"
	DefaultRemoteConfigDocumentID = "default"
	DefaultRemoteConfigInterval   = time.Hour
)

// Agent / task-queue defaults.
const (
	DefaultTasksCollection     = "_tango_tasks"
	DefaultInstancesCollection = "_tango_instances"
	DefaultAgentPollInterval   = 10 * time.Second
	DefaultAgentLeaseDuration  = 5 * time.Minute
	DefaultHeartbeatInterval   = 30 * time.Second
	DefaultInstanceTTL         = 90 * time.Second
)

// TailMode constants control how the tailer watches for file changes.
const (
	// TailModeHybrid uses hpcloud/tail's event-driven watcher as the
	// primary mechanism, with a periodic poll fallback that detects missed
	// notifications. Combines low latency with reliability.
	TailModeHybrid = "hybrid"

	// TailModePoll uses a simple polling loop (read → sleep → retry).
	// Immune to notification-drop races; suitable for all workloads.
	TailModePoll = "poll"

	// TailModeEvent uses hpcloud/tail's kqueue/inotify event-driven watcher.
	// Lowest latency but may stall under sustained concurrent writes due to
	// a known sendOnlyIfEmpty race in the upstream library.
	TailModeEvent = "event"
)

// Config is the top-level configuration. Settings are grouped by concern into
// nested sections (logging, mongo, source, pipeline, filter, backfill,
// remoteConfig, agent); only truly process-wide values (mode, instanceID) stay
// at the top level. YAML keys are the mapstructure tags; env vars use the
// TANGO_ prefix with "." → "_" (e.g. mongo.uri → TANGO_MONGO_URI).
type Config struct {
	// Mode selects the runtime role: report (default), worker, or backfill.
	Mode string `mapstructure:"mode"`

	// InstanceID uniquely identifies a worker instance within a database
	// namespace (used to target tasks and register heartbeats). It is populated
	// from tasks.instanceID and only used by the worker runtime; other paths
	// ignore it.
	InstanceID string `mapstructure:"instanceID"`

	// Logging configures log output.
	Logging LoggingConfig `mapstructure:"logging"`

	// Mongo configures the MongoDB connection and write-retry behaviour.
	Mongo MongoConfig `mapstructure:"mongo"`

	// Source configures the file-tailing data source (report service).
	Source SourceConfig `mapstructure:"source"`

	// Pipeline configures batching and parallel write workers.
	Pipeline PipelineConfig `mapstructure:"pipeline"`

	// Filter configures the reporting (upload) filter: expr-lang include/exclude
	// rules applied to every record by the report / file-upload / string-upload
	// paths. This is distinct from BackfillFilter.
	Filter FilterConfig `mapstructure:"filter"`

	// BackfillFilter configures the backfill selection filter. Unlike the
	// reporting Filter it never matches on "#type"; it selects the virtual
	// table (event/user) and, for the event table, an optional set of event
	// names. The table lives here (in the filter) rather than as a standalone
	// field on Backfill, because backfill selection is configured as a unit.
	BackfillFilter BackfillFilterConfig `mapstructure:"backfillFilter"`

	// Backfill configures the `tango backfill` mode that pulls historical data
	// from ThinkingData's OpenAPI (async SQL endpoints) and routes the rows
	// through the same parse → filter → write pipeline as the report service.
	// Only consulted when the backfill subcommand is invoked.
	Backfill BackfillConfig `mapstructure:"backfill"`

	// RemoteConfig enables a control-plane override: a single JSON document in
	// MongoDB whose fields are merged on top of this configuration at startup
	// (per-field; absent fields keep their local value). When the report service
	// enables it, the document is re-fetched every SyncInterval and the filter is
	// hot-reloaded without a restart, so a data centre can progressively widen
	// which records are collected. Connection fields (mongo.uri, remoteConfig
	// itself) are never overridable remotely — they must come from the local file.
	RemoteConfig RemoteConfig `mapstructure:"remoteConfig"`

	// Agent holds the task-queue runtime settings (projected from the tasks
	// config section). The worker service registers a heartbeat, claims tasks
	// from a shared MongoDB queue, executes them (report-sync / backfill / sql),
	// and reports results.
	Agent AgentConfig `mapstructure:"agent"`
}

// LoggingConfig configures log output.
type LoggingConfig struct {
	// Level is the log verbosity: debug, info, warn, error. Default "info".
	Level string `mapstructure:"level"`
	// Format selects the log encoding: "text" (default) or "json".
	Format string `mapstructure:"format"`
}

// MongoConfig configures the MongoDB connection and write-retry behaviour.
type MongoConfig struct {
	// URI is the MongoDB connection URI (required). The database name is taken
	// from the URI path.
	URI string `mapstructure:"uri"`
	// MaxElapsedTime is the maximum total retry time for a single bulk write.
	// Default 10s.
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`
	// ConnectTimeout bounds the initial connection handshake. Default 10s.
	ConnectTimeout time.Duration `mapstructure:"connectTimeout"`
	// ServerSelectionTimeout bounds how long the driver waits for a suitable
	// server before failing an operation. Default 30s.
	ServerSelectionTimeout time.Duration `mapstructure:"serverSelectionTimeout"`
}

// SourceConfig configures the file-tailing data source (report service).
type SourceConfig struct {
	// LogPattern is a list of glob/regex patterns matched against file paths.
	// Required for the report service; ignored by the worker / backfill paths.
	LogPattern []string `mapstructure:"logPattern"`
	// TailMode selects the file-tailing strategy: hybrid (default) / poll / event.
	TailMode string `mapstructure:"tailMode"`
	// RescanInterval is how often the tailer rescans for new files.
	// Default 30s.
	RescanInterval time.Duration `mapstructure:"rescanInterval"`
	// PollInterval is the poll cadence used by poll / hybrid tail modes.
	// Default 200ms.
	PollInterval time.Duration `mapstructure:"pollInterval"`
	// MaxLineBytes caps a single log line's length. Default 10485760 (10 MB).
	MaxLineBytes int `mapstructure:"maxLineBytes"`
}

// PipelineConfig configures batching and parallel write workers.
type PipelineConfig struct {
	// BatchSize is the target number of records per bulk-write flush.
	// The adaptive min/max can be overridden via batchSizeMin/batchSizeMax.
	// Default 1000.
	BatchSize int `mapstructure:"batchSize"`
	// BatchSizeMin is the adaptive lower bound for batch sizing.
	// When 0, it is auto-derived as BatchSize/4 (minimum 1).
	BatchSizeMin int `mapstructure:"batchSizeMin"`
	// BatchSizeMax is the adaptive upper bound for batch sizing.
	// When 0, it is auto-derived as BatchSize*2.
	BatchSizeMax int `mapstructure:"batchSizeMax"`
	// BatchWorkers is the number of parallel write workers. Default 2.
	BatchWorkers int `mapstructure:"batchWorkers"`
	// FlushInterval is how often workers flush partial batches. Default 1s.
	FlushInterval time.Duration `mapstructure:"flushInterval"`
	// ChannelBuffer is the per-worker line-channel buffer. Default 0 means
	// "derive as BatchSize*2".
	ChannelBuffer int `mapstructure:"channelBuffer"`
	// DeadLetterCap is the per-worker dead-letter batch capacity. Default 128.
	DeadLetterCap int `mapstructure:"deadLetterCap"`
}

// BackfillFilterConfig configures the backfill selection filter. It is
// deliberately separate from FilterConfig: a backfill run already targets one
// virtual table, so this filter selects the table and (for the event table)
// constrains on "#event_name" and arbitrary properties — but never on "#type",
// which has no meaning once the table is fixed. The table lives here rather
// than as a standalone Backfill field, because backfill selection is one unit.
type BackfillFilterConfig struct {
	// Table selects which virtual table to query. Allowed values: "event",
	// "user". Defaults to "event".
	Table string `mapstructure:"table"`
	// Events is convenience sugar that, when non-empty (event table only),
	// restricts the backfill to the named "#event_name" values. It is folded
	// into the include expressions as a single `#event_name in [...]` term.
	Events []string `mapstructure:"events"`
	// Include / Exclude are expr-lang expressions evaluated exactly like the
	// reporting filter, but intended for "#event_name" and property predicates
	// (e.g. `country == "CN"`). They are both pushed down to the TA SQL WHERE
	// clause and enforced again by the in-process safety-net filter.
	Include []string `mapstructure:"include"`
	Exclude []string `mapstructure:"exclude"`
}

// IncludeExprs returns the effective include expressions: the explicit Include
// list plus, when Events is set on the event table, a derived
// `#event_name in [...]` term. Returns nil when nothing constrains the rows.
func (b BackfillFilterConfig) IncludeExprs() []string {
	out := append([]string(nil), b.Include...)
	if b.Table != BackfillTableUser && len(b.Events) > 0 {
		quoted := make([]string, 0, len(b.Events))
		for _, e := range b.Events {
			quoted = append(quoted, strconv.Quote(e))
		}
		out = append(out, `#event_name in [`+strings.Join(quoted, ", ")+`]`)
	}
	return out
}

// FilterConfig configures the expr-lang include/exclude rules.
type FilterConfig struct {
	// Include is a list of expr-lang expressions. If non-empty, a parsed
	// record is kept only when at least one expression evaluates to true
	// (OR semantics). An empty list lets every record through this stage.
	// Expressions are evaluated against the flattened record document, so
	// fields like "#type", "#event_name", and any "properties.*" keys are
	// accessible at the top level (e.g. `#type == "track"`).
	Include []string `mapstructure:"include"`
	// Exclude is a list of expr-lang expressions. A parsed record is dropped
	// if any expression evaluates to true. Applied after Include.
	Exclude []string `mapstructure:"exclude"`
}

// AgentConfig holds the task-queue runtime settings for the worker service.
type AgentConfig struct {
	// Enabled turns the task worker on (set by the worker loader).
	Enabled bool `mapstructure:"enabled"`

	// TasksCollection holds the task queue documents. Defaults "_tango_tasks".
	TasksCollection string `mapstructure:"tasksCollection"`

	// InstancesCollection holds instance heartbeat documents (TTL-expired).
	// Defaults "_tango_instances".
	InstancesCollection string `mapstructure:"instancesCollection"`

	// PollInterval is how often the agent polls for claimable tasks.
	// Defaults 10s.
	PollInterval time.Duration `mapstructure:"pollInterval"`

	// LeaseDuration is how long a claimed task is owned before its lease must
	// be renewed; an expired lease lets another agent reclaim the task.
	// Defaults 5m.
	LeaseDuration time.Duration `mapstructure:"leaseDuration"`

	// HeartbeatInterval is how often the agent refreshes its instance
	// registration. Defaults 30s.
	HeartbeatInterval time.Duration `mapstructure:"heartbeatInterval"`

	// InstanceTTL is the heartbeat document's TTL: an instance is considered
	// offline once its last heartbeat is older than this. Defaults 90s.
	InstanceTTL time.Duration `mapstructure:"instanceTTL"`
}

// RemoteConfig controls the MongoDB-backed configuration override.
type RemoteConfig struct {
	// Enabled turns the remote override on. When false, the local file/env/flag
	// configuration is used verbatim and MongoDB is never consulted for config.
	Enabled bool `mapstructure:"enabled"`

	// Collection is the MongoDB collection holding the config document.
	// Defaults to "_tango_config".
	Collection string `mapstructure:"collection"`

	// DocumentID is the _id of the single shared config document.
	// Defaults to "default".
	DocumentID string `mapstructure:"documentID"`

	// SyncInterval is how often the report service re-fetches the document to
	// hot-reload the filter. Defaults to 1h.
	SyncInterval time.Duration `mapstructure:"syncInterval"`
}

// BackfillConfig holds settings for the historical-data backfill mode.
type BackfillConfig struct {
	// APIBaseURL is the ThinkingData OpenAPI gateway, e.g.
	// "https://ta-receiver.example.com". No trailing slash.
	APIBaseURL string `mapstructure:"apiBaseURL"`

	// Token authenticates against the OpenAPI; passed via ?token=... on every
	// request. One token per TA project.
	Token string `mapstructure:"token"`

	// ProjectID identifies the TA project; used to construct the table name
	// (v_event_<id> / v_user_<id>).
	ProjectID int `mapstructure:"projectID"`

	// PartDateRange is the inclusive [start, end] partition-date range that
	// drives the per-day chunking. Format "YYYY-MM-DD". Required.
	PartDateRange DateRange `mapstructure:"partDateRange"`

	// EventTimeRange, if set, further narrows each day's query with a
	// "#event_time" predicate. Format "YYYY-MM-DD HH:MM:SS". Optional.
	EventTimeRange TimeRange `mapstructure:"eventTimeRange"`

	// PageSize controls server-side pagination of the SQL result set. When
	// Paginate is true it is sent on /open/submit-sql, so the TA OpenAPI
	// splits results into ceil(rowCount/pageSize) pages that the runner
	// fetches and checkpoints one at a time. Must be >= 1000 per the TA
	// documentation; defaults to 10000.
	PageSize int `mapstructure:"pageSize"`

	// Paginate selects the result-retrieval mode:
	//
	//   true  (default) — submit with pageSize so the server paginates;
	//                      the runner pulls page 0..pageCount-1 and writes a
	//                      checkpoint after each page (resumable mid-table).
	//   false           — submit without pageSize; the server streams the
	//                      entire result set as one response that the runner
	//                      consumes row-by-row, flushing batches as it goes.
	//                      A mid-stream failure restarts the whole chunk
	//                      (dedup via #uuid / #user_id keeps it correct).
	//
	// Both modes stream rows incrementally and never buffer a full page in
	// memory; the difference is resume granularity vs. one fewer round trip.
	Paginate *bool `mapstructure:"paginate"`

	// PollInterval is the gap between /open/sql-task-info polls. Defaults 3s.
	PollInterval time.Duration `mapstructure:"pollInterval"`

	// PollTimeout caps how long a single day's task may take from submit to
	// FINISHED before the runner aborts that day. Defaults 30m.
	PollTimeout time.Duration `mapstructure:"pollTimeout"`

	// RunID is a stable identifier for this backfill run; doubles as the
	// _backfill_progress document _id, allowing resume across restarts.
	// Required.
	RunID string `mapstructure:"runID"`

	// ProgressCollection names the Mongo collection used for checkpoint
	// storage. Defaults to "_backfill_progress".
	ProgressCollection string `mapstructure:"progressCollection"`

	// ForceSkipExisting causes the event write path to use $setOnInsert
	// regardless of the record's #type, so duplicate #uuid rows are skipped
	// instead of mutated. Defaults true (recommended for backfill).
	ForceSkipExisting *bool `mapstructure:"forceSkipExisting"`

	// SkipLocalFilter disables the in-process filter safety net (only the
	// pushed-down Presto WHERE clause is applied). Defaults false; set to
	// true only if you trust the SQL pushdown semantically.
	SkipLocalFilter bool `mapstructure:"skipLocalFilter"`

	// Proxy is an optional outbound proxy URL for HTTP calls to the TA
	// OpenAPI. Accepts http://, https://, and socks5://. Authentication can
	// be embedded as user:pass@host:port. Empty means direct connection.
	Proxy string `mapstructure:"proxy"`

	// SchemaPrefix is prepended to the virtual table name in the FROM clause,
	// e.g. setting it to "ta" yields FROM ta.v_event_<pid>. Empty (default)
	// means the table is referenced without a schema.
	SchemaPrefix string `mapstructure:"schemaPrefix"`

	// Limit, when positive, appends LIMIT <n> to every issued SQL statement.
	// Intended for smoke tests — produces a bounded result regardless of the
	// table's true size. Leave 0 (default) for production runs.
	Limit int `mapstructure:"limit"`

	// PageRetries is how many times a single result page is re-fetched on a
	// transient HTTP/decode error before the whole chunk is failed. Re-fetching
	// a page is safe because #uuid / #user_id upserts dedup already-written
	// rows. Default 3.
	PageRetries int `mapstructure:"pageRetries"`
}

// DateRange is an inclusive [start, end] date interval in "YYYY-MM-DD" form.
type DateRange struct {
	Start string `mapstructure:"start"`
	End   string `mapstructure:"end"`
}

// TimeRange is an inclusive [start, end] timestamp interval in
// "YYYY-MM-DD HH:MM:SS" form. Zero values mean "no bound on that side".
type TimeRange struct {
	Start string `mapstructure:"start"`
	End   string `mapstructure:"end"`
}

// Empty reports whether neither bound is set.
func (r TimeRange) Empty() bool { return r.Start == "" && r.End == "" }

// ForceSkip returns the effective value of ForceSkipExisting, defaulting to
// true when the pointer is nil (i.e. user omitted the field).
func (b *BackfillConfig) ForceSkip() bool {
	if b.ForceSkipExisting == nil {
		return true
	}
	return *b.ForceSkipExisting
}

// BatchSizeMin returns the adaptive lower bound for batch sizing.
// When BatchSizeMin is explicitly set (>0), it is used directly (clamped to BatchSize).
// When BatchSizeMin is 0, it is auto-derived as BatchSize/4 (minimum 1).
func (c Config) BatchSizeMin() int {
	if c.Pipeline.BatchSizeMin > 0 {
		if c.Pipeline.BatchSizeMin > c.Pipeline.BatchSize {
			return c.Pipeline.BatchSize
		}
		return c.Pipeline.BatchSizeMin
	}
	v := c.Pipeline.BatchSize / 4
	if v < 1 {
		return 1
	}
	return v
}

// BatchSizeMax returns the adaptive upper bound for batch sizing.
// When BatchSizeMax is explicitly set (>0), it is used directly (clamped to BatchSize).
// When BatchSizeMax is 0, it is auto-derived as BatchSize*2.
func (c Config) BatchSizeMax() int {
	if c.Pipeline.BatchSizeMax > 0 {
		if c.Pipeline.BatchSizeMax < c.Pipeline.BatchSize {
			return c.Pipeline.BatchSize
		}
		return c.Pipeline.BatchSizeMax
	}
	return c.Pipeline.BatchSize * 2
}

// BatchChannelSize returns the per-worker channel buffer size. It honours an
// explicit pipeline.channelBuffer when set, otherwise derives BatchSize*2.
func (c Config) BatchChannelSize() int {
	if c.Pipeline.ChannelBuffer > 0 {
		return c.Pipeline.ChannelBuffer
	}
	return c.Pipeline.BatchSize * 2
}

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
	applyAgentDefaults(&c.Agent)
}

func applyAgentDefaults(a *AgentConfig) {
	if a.TasksCollection == "" {
		a.TasksCollection = DefaultTasksCollection
	}
	if a.InstancesCollection == "" {
		a.InstancesCollection = DefaultInstancesCollection
	}
	if a.PollInterval <= 0 {
		a.PollInterval = DefaultAgentPollInterval
	}
	if a.LeaseDuration <= 0 {
		a.LeaseDuration = DefaultAgentLeaseDuration
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

// ShouldPaginate reports the effective pagination mode, defaulting to true
// when the user omits the field.
func (b *BackfillConfig) ShouldPaginate() bool {
	return b.Paginate == nil || *b.Paginate
}

// EffectivePageSize returns the pageSize to pass to submit-sql: PageSize in
// paginated mode, or 0 (no server-side pagination) when Paginate is false.
func (b *BackfillConfig) EffectivePageSize() int {
	if b.ShouldPaginate() {
		return b.PageSize
	}
	return 0
}

// Validate checks that required fields are present.
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeReport, ModeWorker, ModeBackfill:
		// valid
	default:
		return fmt.Errorf("config: mode must be one of %q, %q, %q; got %q",
			ModeReport, ModeWorker, ModeBackfill, c.Mode)
	}
	if c.Mode == ModeWorker && c.InstanceID == "" {
		return fmt.Errorf("config: worker mode requires tasks.instanceID to be set")
	}
	if c.Mongo.URI == "" {
		return fmt.Errorf("config: mongo.uri is required (set runtime.mongo.uri in the config file, via TANGO_RUNTIME_MONGO_URI, or via --runtime.mongo.uri)")
	}
	switch c.Source.TailMode {
	case TailModeHybrid, TailModePoll, TailModeEvent:
		// valid
	default:
		return fmt.Errorf("config: source.tailMode must be %q, %q or %q; got %q",
			TailModeHybrid, TailModePoll, TailModeEvent, c.Source.TailMode)
	}
	// Validate batch size constraints.
	if c.Pipeline.BatchSizeMin > 0 && c.Pipeline.BatchSizeMin > c.Pipeline.BatchSize {
		return fmt.Errorf("config: pipeline.batchSizeMin (%d) cannot exceed pipeline.batchSize (%d)",
			c.Pipeline.BatchSizeMin, c.Pipeline.BatchSize)
	}
	if c.Pipeline.BatchSizeMax > 0 && c.Pipeline.BatchSize > c.Pipeline.BatchSizeMax {
		return fmt.Errorf("config: pipeline.batchSize (%d) cannot exceed pipeline.batchSizeMax (%d)",
			c.Pipeline.BatchSize, c.Pipeline.BatchSizeMax)
	}
	if _, err := filter.New(c.Filter.Include, c.Filter.Exclude); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// Pre-compile filter expressions to SQL only when the backfill mode is in
	// play; otherwise we don't want a malformed SQL pushdown to block the
	// report path.
	if c.Mode == ModeBackfill {
		switch c.BackfillFilter.Table {
		case BackfillTableEvent, BackfillTableUser:
			// valid
		default:
			return fmt.Errorf("config: backfillFilter.table must be %q or %q; got %q",
				BackfillTableEvent, BackfillTableUser, c.BackfillFilter.Table)
		}
		if err := c.Backfill.validate(c.BackfillFilter.Table); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if _, err := filter.New(c.BackfillFilter.IncludeExprs(), c.BackfillFilter.Exclude); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if _, err := filter.CompileToSQL(c.BackfillFilter.IncludeExprs(), c.BackfillFilter.Exclude); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	return nil
}

func (b *BackfillConfig) validate(table string) error {
	if b.APIBaseURL == "" {
		return fmt.Errorf("backfill.apiBaseURL is required")
	}
	if !strings.HasPrefix(b.APIBaseURL, "http://") && !strings.HasPrefix(b.APIBaseURL, "https://") {
		return fmt.Errorf("backfill.apiBaseURL must start with http(s)://; got %q", b.APIBaseURL)
	}
	if b.Token == "" {
		return fmt.Errorf("backfill.token is required")
	}
	if b.ProjectID <= 0 {
		return fmt.Errorf("backfill.projectID must be a positive integer")
	}
	if b.RunID == "" {
		return fmt.Errorf("backfill.runID is required (used as resume key)")
	}
	if b.PageSize < 1000 {
		return fmt.Errorf("backfill.pageSize must be >= 1000 (TA OpenAPI minimum)")
	}
	// User tables in TA do not have a $part_date partition column, so the
	// date range is required only for the event table.
	if table == BackfillTableEvent {
		if _, err := time.Parse("2006-01-02", b.PartDateRange.Start); err != nil {
			return fmt.Errorf("backfill.partDateRange.start invalid (want YYYY-MM-DD): %w", err)
		}
		if _, err := time.Parse("2006-01-02", b.PartDateRange.End); err != nil {
			return fmt.Errorf("backfill.partDateRange.end invalid (want YYYY-MM-DD): %w", err)
		}
	}
	if b.Proxy != "" {
		u, err := url.Parse(b.Proxy)
		if err != nil {
			return fmt.Errorf("backfill.proxy invalid: %w", err)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return fmt.Errorf("backfill.proxy scheme %q not supported (http/https/socks5 only)", u.Scheme)
		}
	}
	if !b.EventTimeRange.Empty() {
		if b.EventTimeRange.Start != "" {
			if _, err := time.Parse("2006-01-02 15:04:05", b.EventTimeRange.Start); err != nil {
				return fmt.Errorf("backfill.eventTimeRange.start invalid (want YYYY-MM-DD HH:MM:SS): %w", err)
			}
		}
		if b.EventTimeRange.End != "" {
			if _, err := time.Parse("2006-01-02 15:04:05", b.EventTimeRange.End); err != nil {
				return fmt.Errorf("backfill.eventTimeRange.end invalid (want YYYY-MM-DD HH:MM:SS): %w", err)
			}
		}
	}
	return nil
}

// BuildFilter compiles the configured filter expressions. Validate must have
// been called first; this method is intended to be invoked by runtime
// components (the report service / upload paths) that need a ready-to-use filter.
func (c *Config) BuildFilter() (*filter.Filter, error) {
	return filter.New(c.Filter.Include, c.Filter.Exclude)
}

// BuildBackfillFilter compiles the backfill selection filter into a local
// (in-process) filter used as a safety net behind the SQL pushdown. It is
// driven by BackfillFilter (table + events), never by the reporting Filter.
func (c *Config) BuildBackfillFilter() (*filter.Filter, error) {
	return filter.New(c.BackfillFilter.IncludeExprs(), c.BackfillFilter.Exclude)
}

// BackfillWhere renders the backfill filter as a Presto WHERE-clause body
// (no leading WHERE), pushing the event-name predicate down to the TA OpenAPI.
func (c *Config) BackfillWhere() (string, error) {
	return filter.CompileToSQL(c.BackfillFilter.IncludeExprs(), c.BackfillFilter.Exclude)
}

// MongoDBFromURI extracts the database name from a MongoDB URI path.
// Examples:
//   - mongodb://host:27017/tango => "tango"
//   - mongodb://host:27017       => "tango" (default fallback)
func MongoDBFromURI(uri string) (string, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("parse mongo uri: %w", err)
	}
	db := strings.Trim(u.Path, "/")
	if db == "" {
		return "tango", nil
	}
	return db, nil
}
