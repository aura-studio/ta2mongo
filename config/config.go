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

// Config is the flat top-level configuration.
// All fields map directly to YAML keys, CLI flags, and TANGO_* env vars.
type Config struct {
	// Mode selects the run mode: daemon (default), once, or ingest.
	Mode string `mapstructure:"mode"`

	// MongoURI is the MongoDB connection URI (required).
	// The database name is extracted from the URI path.
	MongoURI string `mapstructure:"mongoURI"`

	// LogPattern is a list of regex patterns matched against file paths.
	// Required for daemon and once modes; ignored by ingest.
	LogPattern []string `mapstructure:"logPattern"`

	// RescanInterval is how often the tailer rescans for new files (daemon only).
	RescanInterval time.Duration `mapstructure:"rescanInterval"`

	// BatchSize is the target number of records per bulk-write flush.
	// The adaptive min is BatchSize/4 and max is BatchSize*2.
	BatchSize int `mapstructure:"batchSize"`

	// BatchWorkers is the number of parallel write workers.
	BatchWorkers int `mapstructure:"batchWorkers"`

	// FlushInterval is how often workers flush partial batches (e.g. "1s").
	FlushInterval time.Duration `mapstructure:"flushInterval"`

	// MaxElapsedTime is the maximum total retry time for a single bulk write.
	MaxElapsedTime time.Duration `mapstructure:"maxElapsedTime"`

	// LogLevel is the log verbosity: debug, info, warn, error.
	LogLevel string `mapstructure:"logLevel"`

	// TailMode selects the file-tailing strategy:
	//   hybrid (default) — event-driven with poll fallback; low latency + reliable.
	//   poll             — pure polling, immune to notification-drop races.
	//   event            — pure kqueue/inotify, lowest latency but may stall.
	TailMode string `mapstructure:"tailMode"`

	// FilterInclude is a list of expr-lang expressions. If non-empty, a parsed
	// record is kept only when at least one expression evaluates to true
	// (OR semantics). An empty list lets every record through this stage.
	// Expressions are evaluated against the flattened record document, so
	// fields like "#type", "#event_name", and any "properties.*" keys are
	// accessible at the top level (e.g. `#type == "track"`).
	FilterInclude []string `mapstructure:"filterInclude"`

	// FilterExclude is a list of expr-lang expressions. A parsed record is
	// dropped if any expression evaluates to true. Applied after FilterInclude.
	FilterExclude []string `mapstructure:"filterExclude"`

	// Backfill configures the `tango backfill` mode that pulls historical data
	// from ThinkingData's OpenAPI (async SQL endpoints) and routes the rows
	// through the same parse → filter → write pipeline as daemon/once. Only
	// consulted when the backfill subcommand is invoked.
	Backfill BackfillConfig `mapstructure:"backfill"`
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
	v := c.BatchSize / 4
	if v < 1 {
		return 1
	}
	return v
}

// BatchSizeMax returns the adaptive upper bound (BatchSize * 2).
func (c Config) BatchSizeMax() int {
	return c.BatchSize * 2
}

// BatchChannelSize returns the per-worker channel buffer size (BatchSize * 2).
func (c Config) BatchChannelSize() int {
	return c.BatchSize * 2
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

// bindFlags binds every flag in the set to its matching viper key.
// Only flags explicitly set on the CLI take effect; unset flags
// fall back to the file / env / default chain.
func bindFlags(v *viper.Viper, flags *pflag.FlagSet) error {
	var bindErr error
	flags.VisitAll(func(f *pflag.Flag) {
		if bindErr != nil {
			return
		}
		if err := v.BindPFlag(f.Name, f); err != nil {
			bindErr = fmt.Errorf("bind flag %q: %w", f.Name, err)
		}
	})
	return bindErr
}

// setDefaults registers viper defaults for all fields.
func setDefaults(v *viper.Viper) {
	v.SetDefault("mode", ModeDaemon)
	v.SetDefault("mongoURI", "")
	v.SetDefault("logPattern", []string{})
	v.SetDefault("rescanInterval", "30s")
	v.SetDefault("batchSize", 1000)
	v.SetDefault("batchWorkers", 2)
	v.SetDefault("flushInterval", "1s")
	v.SetDefault("maxElapsedTime", "10s")
	v.SetDefault("logLevel", "info")
	v.SetDefault("tailMode", TailModeHybrid)
	v.SetDefault("filterInclude", []string{})
	v.SetDefault("filterExclude", []string{})
}

// applyDefaults fills in zero-value fields with sensible defaults.
func applyDefaults(c *Config) {
	if c.Mode == "" {
		c.Mode = ModeDaemon
	}
	if c.RescanInterval <= 0 {
		c.RescanInterval = 30 * time.Second
	}
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.BatchWorkers <= 0 {
		c.BatchWorkers = 2
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = time.Second
	}
	if c.MaxElapsedTime <= 0 {
		c.MaxElapsedTime = 10 * time.Second
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.TailMode == "" {
		c.TailMode = TailModeHybrid
	}
	applyBackfillDefaults(&c.Backfill)
}

func applyBackfillDefaults(b *BackfillConfig) {
	if b.Table == "" {
		b.Table = BackfillTableEvent
	}
	if b.PageSize <= 0 {
		b.PageSize = 10000
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
	case ModeDaemon, ModeOnce, ModeIngest, ModeBackfill:
		// valid
	default:
		return fmt.Errorf("config: mode must be one of %q, %q, %q, %q; got %q",
			ModeDaemon, ModeOnce, ModeIngest, ModeBackfill, c.Mode)
	}
	if c.MongoURI == "" {
		return fmt.Errorf("config: mongoURI is required (set via --mongoURI, TANGO_MONGOURI, or config file)")
	}
	switch c.TailMode {
	case TailModeHybrid, TailModePoll, TailModeEvent:
		// valid
	default:
		return fmt.Errorf("config: tailMode must be %q, %q or %q; got %q",
			TailModeHybrid, TailModePoll, TailModeEvent, c.TailMode)
	}
	if _, err := filter.New(c.FilterInclude, c.FilterExclude); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// Pre-compile filter expressions to SQL only when the backfill mode is in
	// play; otherwise we don't want a malformed SQL pushdown to block the
	// daemon/once/ingest paths.
	if c.Mode == ModeBackfill {
		if err := c.Backfill.validate(); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if _, err := filter.CompileToSQL(c.FilterInclude, c.FilterExclude); err != nil {
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
	return filter.New(c.FilterInclude, c.FilterExclude)
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
