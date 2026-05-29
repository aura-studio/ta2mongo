// Package config defines the tango configuration structure and loading logic.
//
// Configuration is loaded from four sources in increasing priority order:
//  1. Built-in defaults
//  2. YAML config file (optional; skipped silently if the file does not exist)
//  3. Environment variables (prefix: TANGO_, e.g. TANGO_MONGOURI)
//  4. CLI flags (highest priority)
//
// All YAML keys and CLI flag names are flat camelCase, e.g.
//
//	mongoURI        => TANGO_MONGOURI         / --mongoURI
//	logLevel        => TANGO_LOGLEVEL         / --logLevel
//	batchSize       => TANGO_BATCHSIZE        / --batchSize
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"rocket-nano/tools/tango/internal/filter"
)

// Mode constants for the run mode configuration.
const (
	ModeDaemon   = "daemon"
	ModeOnce     = "once"
	ModeIngest   = "ingest"
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

// ModeAgent is the run mode for the task-worker subcommand.
const ModeAgent = "agent"

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
	// Mode selects the run mode: daemon (default), once, ingest, backfill, agent.
	Mode string `mapstructure:"mode"`

	// InstanceID uniquely identifies this tango process within a database
	// namespace. Sourced from the TANGO_INSTANCE_ID environment variable.
	// Required for the `tango agent` task-worker mode (used to target tasks at
	// a specific instance and to register heartbeats); other modes ignore it.
	InstanceID string `mapstructure:"instanceID"`

	// Logging configures log output.
	Logging LoggingConfig `mapstructure:"logging"`

	// Mongo configures the MongoDB connection and write-retry behaviour.
	Mongo MongoConfig `mapstructure:"mongo"`

	// Source configures the file-tailing data source (daemon/once modes).
	Source SourceConfig `mapstructure:"source"`

	// Pipeline configures batching and parallel write workers.
	Pipeline PipelineConfig `mapstructure:"pipeline"`

	// Filter configures expr-lang include/exclude rules applied to every record.
	Filter FilterConfig `mapstructure:"filter"`

	// Backfill configures the `tango backfill` mode that pulls historical data
	// from ThinkingData's OpenAPI (async SQL endpoints) and routes the rows
	// through the same parse → filter → write pipeline as daemon/once. Only
	// consulted when the backfill subcommand is invoked.
	Backfill BackfillConfig `mapstructure:"backfill"`

	// RemoteConfig enables a control-plane override: a single JSON document in
	// MongoDB whose fields are merged on top of this configuration at startup
	// (per-field; absent fields keep their local value). In daemon mode the
	// document is re-fetched every SyncInterval and the filter is hot-reloaded
	// without a restart, so a data centre can progressively widen which records
	// are collected. Connection fields (mongo.uri, remoteConfig itself) are
	// never overridable remotely — they must come from the local file.
	RemoteConfig RemoteConfig `mapstructure:"remoteConfig"`

	// Agent configures the `tango agent` task-worker mode: a long-running
	// process that registers a heartbeat, claims tasks published to a shared
	// MongoDB queue, executes them (backfill / sql), and reports results.
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

// SourceConfig configures the file-tailing data source (daemon/once modes).
type SourceConfig struct {
	// LogPattern is a list of glob/regex patterns matched against file paths.
	// Required for daemon and once modes; ignored by ingest/backfill/agent.
	LogPattern []string `mapstructure:"logPattern"`
	// TailMode selects the file-tailing strategy: hybrid (default) / poll / event.
	TailMode string `mapstructure:"tailMode"`
	// RescanInterval is how often the tailer rescans for new files (daemon).
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
	// The adaptive min is BatchSize/4 and max is BatchSize*2. Default 1000.
	BatchSize int `mapstructure:"batchSize"`
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

// AgentConfig controls the task-worker (`tango agent`) mode.
type AgentConfig struct {
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

	// SyncInterval is how often daemon mode re-fetches the document to
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

	// Table selects which virtual table to query. Allowed values: "event",
	// "user". Defaults to "event".
	Table string `mapstructure:"table"`

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

// BatchSizeMin returns the adaptive lower bound (BatchSize / 4, minimum 1).
func (c Config) BatchSizeMin() int {
	v := c.Pipeline.BatchSize / 4
	if v < 1 {
		return 1
	}
	return v
}

// BatchSizeMax returns the adaptive upper bound (BatchSize * 2).
func (c Config) BatchSizeMax() int {
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


// Load builds a Config from defaults → YAML file → env vars → CLI flags.
//
// If path is empty or the file does not exist, file loading is skipped silently.
func Load(path string, flags *pflag.FlagSet) (Config, error) {
	v := viper.New()

	// Environment variables: TANGO_MONGOURI, TANGO_LOGLEVEL, etc.
	v.SetEnvPrefix("TANGO")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// instanceID maps to TANGO_INSTANCE_ID. AutomaticEnv would look for
	// TANGO_INSTANCEID (no separator), so bind the documented name explicitly.
	_ = v.BindEnv("instanceID", "TANGO_INSTANCE_ID")

	// Register defaults.
	setDefaults(v)

	// Load YAML file (optional).
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			v.SetConfigFile(path)
			if err := v.ReadInConfig(); err != nil {
				return Config{}, fmt.Errorf("read config %q: %w", path, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("stat config %q: %w", path, err)
		}
		// ErrNotExist: silently skip; use defaults + env + flags.
	}

	// Bind CLI flags (flags override env vars and file).
	if flags != nil {
		if err := bindFlags(v, flags); err != nil {
			return Config{}, err
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshal config: %w", err)
	}

	applyDefaults(&cfg)
	return cfg, nil
}

// FlagKeyMap maps CLI flag names to their (possibly nested) viper config keys.
// Callers (the cmd package) supply it so config need not know flag specifics.
type FlagKeyMap map[string]string

// flagKeys is set by LoadWithFlagKeys; bindFlags consults it to translate flag
// names into nested viper keys. Defaults to identity mapping when nil.
var flagKeys FlagKeyMap

// bindFlags binds every flag in the set to its matching viper key, translating
// known flag names to nested keys via flagKeys. Only flags explicitly set on
// the CLI take effect; unset flags fall back to the file / env / default chain.
func bindFlags(v *viper.Viper, flags *pflag.FlagSet) error {
	var bindErr error
	flags.VisitAll(func(f *pflag.Flag) {
		if bindErr != nil {
			return
		}
		key := f.Name
		if mapped, ok := flagKeys[f.Name]; ok {
			key = mapped
		}
		if err := v.BindPFlag(key, f); err != nil {
			bindErr = fmt.Errorf("bind flag %q: %w", f.Name, err)
		}
	})
	return bindErr
}

// SetFlagKeyMap installs the flag→config-key translation used by Load. The cmd
// package calls this once at init.
func SetFlagKeyMap(m FlagKeyMap) { flagKeys = m }

// setDefaults registers viper defaults for all fields.
func setDefaults(v *viper.Viper) {
	v.SetDefault("mode", ModeDaemon)
	v.SetDefault("logging.level", "info")
	v.SetDefault("logging.format", "text")
	v.SetDefault("mongo.uri", "")
	v.SetDefault("mongo.maxElapsedTime", "10s")
	v.SetDefault("mongo.connectTimeout", "10s")
	v.SetDefault("mongo.serverSelectionTimeout", "30s")
	v.SetDefault("source.logPattern", []string{})
	v.SetDefault("source.tailMode", TailModeHybrid)
	v.SetDefault("source.rescanInterval", "30s")
	v.SetDefault("source.pollInterval", "200ms")
	v.SetDefault("source.maxLineBytes", 10*1024*1024)
	v.SetDefault("pipeline.batchSize", 1000)
	v.SetDefault("pipeline.batchWorkers", 2)
	v.SetDefault("pipeline.flushInterval", "1s")
	v.SetDefault("pipeline.channelBuffer", 0)
	v.SetDefault("pipeline.deadLetterCap", 128)
	v.SetDefault("filter.include", []string{})
	v.SetDefault("filter.exclude", []string{})
}

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(c *Config) {
	if c.Mode == "" {
		c.Mode = ModeDaemon
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

func applyBackfillDefaults(b *BackfillConfig) {
	if b.Table == "" {
		b.Table = BackfillTableEvent
	}
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
	case ModeDaemon, ModeOnce, ModeIngest, ModeBackfill, ModeAgent:
		// valid
	default:
		return fmt.Errorf("config: mode must be one of %q, %q, %q, %q, %q; got %q",
			ModeDaemon, ModeOnce, ModeIngest, ModeBackfill, ModeAgent, c.Mode)
	}
	if c.Mode == ModeAgent && c.InstanceID == "" {
		return fmt.Errorf("config: agent mode requires TANGO_INSTANCE_ID to be set")
	}
	if c.Mongo.URI == "" {
		return fmt.Errorf("config: mongo.uri is required (set via --mongoURI, TANGO_MONGO_URI, or config file)")
	}
	switch c.Source.TailMode {
	case TailModeHybrid, TailModePoll, TailModeEvent:
		// valid
	default:
		return fmt.Errorf("config: source.tailMode must be %q, %q or %q; got %q",
			TailModeHybrid, TailModePoll, TailModeEvent, c.Source.TailMode)
	}
	if _, err := filter.New(c.Filter.Include, c.Filter.Exclude); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// Pre-compile filter expressions to SQL only when the backfill mode is in
	// play; otherwise we don't want a malformed SQL pushdown to block the
	// daemon/once/ingest paths.
	if c.Mode == ModeBackfill {
		if err := c.Backfill.validate(); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if _, err := filter.CompileToSQL(c.Filter.Include, c.Filter.Exclude); err != nil {
			return fmt.Errorf("config: %w", err)
		}
	}
	return nil
}

func (b *BackfillConfig) validate() error {
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
	switch b.Table {
	case BackfillTableEvent, BackfillTableUser:
		// valid
	default:
		return fmt.Errorf("backfill.table must be %q or %q; got %q",
			BackfillTableEvent, BackfillTableUser, b.Table)
	}
	if b.RunID == "" {
		return fmt.Errorf("backfill.runID is required (used as resume key)")
	}
	if b.PageSize < 1000 {
		return fmt.Errorf("backfill.pageSize must be >= 1000 (TA OpenAPI minimum)")
	}
	// User tables in TA do not have a $part_date partition column, so the
	// date range is required only for the event table.
	if b.Table == BackfillTableEvent {
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
// components (daemon, once, ingest) that need a ready-to-use filter.
func (c *Config) BuildFilter() (*filter.Filter, error) {
	return filter.New(c.Filter.Include, c.Filter.Exclude)
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
