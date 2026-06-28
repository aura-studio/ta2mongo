package backfill

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// Table identifiers and pagination bounds for the backfill source.
const (
	// TableEvent selects the partitioned event table v_event_<projectID>.
	TableEvent = "event"
	// TableUser selects the unpartitioned user-state table v_user_<projectID>.
	TableUser = "user"
	// UserChunkKey is the single virtual "day" used for the user table, which is
	// unpartitioned and fetched as one task.
	UserChunkKey = "user-full"
	// minPageSize is the TA OpenAPI's minimum server-side page size.
	minPageSize = 1000
	// defaultPageSize is the page size used when none is configured.
	defaultPageSize = 10000

	// typeEvent / typeUserSet / typeUserSetOnce are the TA record #type values
	// injected when a fetched row lacks one, so the row flows through the normal
	// parse → identity → write pipeline (events as track upserts; user-state
	// rows as user_set / user_setOnce updates — no custom write model needed).
	typeEvent       = "track"
	typeUserSet     = "user_set"
	typeUserSetOnce = "user_setOnce"

	// defaultUserTimeColumn is the v_user_<id> column copied into #time for
	// user-table rows when none is configured. TA's user table has no column
	// literally named "#time" (that is an event concept); its per-user timestamp
	// is #update_time. See Config.UserTimeColumn and RowKeys.
	defaultUserTimeColumn = "#update_time"
	// defaultEventTimeColumn is the v_event_<id> column copied into #time for
	// event-table rows when none is configured. TA's warehouse event view exposes
	// the event time as #event_time, not the live-ingest #time, so it must be
	// mapped. See Config.EventTimeColumn and RowKeys.
	defaultEventTimeColumn = "#event_time"
)

// DateRange is an inclusive [Start, End] partition-date range (YYYY-MM-DD).
type DateRange struct {
	Start string `mapstructure:"start"`
	End   string `mapstructure:"end"`
}

// TimeRange is an optional event-time bound (YYYY-MM-DD HH:MM:SS).
type TimeRange struct {
	Start string `mapstructure:"start"`
	End   string `mapstructure:"end"`
}

// Empty reports whether neither bound is set.
func (r TimeRange) Empty() bool { return r.Start == "" && r.End == "" }

// Config is the backfill domain's configuration (file key backfill.*). It
// describes how to FETCH historical rows from the ThinkingData OpenAPI — the
// rows are then encoded as TA log lines and ingested through the engine's
// normal upload pipeline (so no custom write model, checkpoint, or selection
// filter lives here: the reporting filter is the engine's parser.filter.*).
type Config struct {
	// --- TA OpenAPI connection ---
	APIBaseURL string `mapstructure:"apiBaseURL"`
	Token      string `mapstructure:"token"`
	Proxy      string `mapstructure:"proxy"`
	ProjectID  int    `mapstructure:"projectID"`

	// --- query selection ---
	Table          string    `mapstructure:"table"` // "event" (default) or "user"
	Events         []string  `mapstructure:"events"`
	SchemaPrefix   string    `mapstructure:"schemaPrefix"`
	PartDateRange  DateRange `mapstructure:"partDateRange"`
	EventTimeRange TimeRange `mapstructure:"eventTimeRange"`
	Limit          int       `mapstructure:"limit"`
	// UserTimeColumn names the v_user_<id> column copied into #time for user-table
	// rows (the user table has no column literally named "#time", but talog
	// requires a non-empty #time for user_* records). Default "#update_time"; when
	// that column is absent too, a per-run synthesized timestamp is used. Ignored
	// for the event table.
	UserTimeColumn string `mapstructure:"userTimeColumn"`
	// EventTimeColumn names the v_event_<id> column copied into #time for
	// event-table rows (the warehouse event view exposes #event_time, not the
	// live-ingest #time, yet talog requires a non-empty #time). Default
	// "#event_time"; when that column is absent too, a per-run synthesized
	// timestamp is used. Ignored for the user table.
	EventTimeColumn string `mapstructure:"eventTimeColumn"`
	// UserOrderBy is an optional ORDER BY clause (column[s] + ASC/DESC, no
	// "ORDER BY" prefix) applied to the user-table query, so a Limit selects a
	// deterministic slice instead of an arbitrary sample — e.g.
	// "last_login_time DESC" to import the N most-recently-logged-in users.
	// Empty = no ordering. Ignored for the event table. Operator-supplied
	// (trusted) SQL, like Events.
	UserOrderBy string `mapstructure:"userOrderBy"`
	// UserWhere is an optional WHERE predicate (no "WHERE" prefix) AND-ed into
	// the user-table query. It is the primitive a distributed orchestrator uses
	// to FETCH ONE SHARD of the user table per worker — the user table is
	// unpartitioned (no $part_date), so an even, complete, disjoint partition is
	// expressed as a predicate, e.g.
	//   mod(cast("#user_id" AS bigint) / 4194304, 8) = 3
	// (drop the snowflake id's low 22 sequence/machine bits — those are skewed —
	// and mod the embedded millisecond timestamp, which is uniform). Empty = no
	// predicate. Ignored for the event table (it shards by $part_date day).
	// Operator-supplied (trusted) SQL, like Events / UserOrderBy.
	UserWhere string `mapstructure:"userWhere"`

	// --- pagination / polling ---
	PageSize     int           `mapstructure:"pageSize"`
	Paginate     *bool         `mapstructure:"paginate"` // nil/true = paginate
	PageRetries  int           `mapstructure:"pageRetries"`
	PollInterval time.Duration `mapstructure:"pollInterval"`
	PollTimeout  time.Duration `mapstructure:"pollTimeout"`

	// --- write semantics ---
	// ForceSkipExisting selects the user-table record type so historical data
	// never overwrites live state: true (default) → user_setOnce ($setOnInsert),
	// false → user_set ($set). It does not affect the event table (events are
	// always #uuid $setOnInsert via the track write model).
	ForceSkipExisting *bool `mapstructure:"forceSkipExisting"`
}

// FromTree decodes the backfill.* branch of t into a Config, applies defaults
// and validates it.
func FromTree(t cfgtree.Tree) (*Config, error) {
	var c Config
	if err := t.Sub("backfill").Into(&c); err != nil {
		return nil, fmt.Errorf("backfill: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("backfill: %w", err)
	}
	return &c, nil
}

// RegisterDefaults registers this module's config keys (under prefix) so env
// binding works. The *bool tri-state fields default to true.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".apiBaseURL", "")
	set(prefix+".token", "")
	set(prefix+".proxy", "")
	set(prefix+".projectID", 0)
	set(prefix+".table", "")
	set(prefix+".events", []string{})
	set(prefix+".schemaPrefix", "")
	set(prefix+".userTimeColumn", "")
	set(prefix+".eventTimeColumn", "")
	set(prefix+".userOrderBy", "")
	set(prefix+".userWhere", "")
	set(prefix+".partDateRange.start", "")
	set(prefix+".partDateRange.end", "")
	set(prefix+".eventTimeRange.start", "")
	set(prefix+".eventTimeRange.end", "")
	set(prefix+".limit", 0)
	set(prefix+".pageSize", 0)
	set(prefix+".paginate", true)
	set(prefix+".pageRetries", 0)
	set(prefix+".pollInterval", "0s")
	set(prefix+".pollTimeout", "0s")
	set(prefix+".forceSkipExisting", true)
}

// ApplyDefaults fills unset backfill options.
func (c *Config) ApplyDefaults() {
	if c.Table == "" {
		c.Table = TableEvent
	}
	if c.UserTimeColumn == "" {
		c.UserTimeColumn = defaultUserTimeColumn
	}
	if c.EventTimeColumn == "" {
		c.EventTimeColumn = defaultEventTimeColumn
	}
	if c.PageSize <= 0 {
		c.PageSize = defaultPageSize
	}
	if c.PageRetries <= 0 {
		c.PageRetries = 3
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 3 * time.Second
	}
	if c.PollTimeout <= 0 {
		c.PollTimeout = 30 * time.Minute
	}
}

// Validate checks the backfill config.
func (c *Config) Validate() error {
	switch c.Table {
	case TableEvent, TableUser:
	default:
		return fmt.Errorf("table %q is invalid (want %q or %q)", c.Table, TableEvent, TableUser)
	}
	if c.APIBaseURL == "" {
		return fmt.Errorf("apiBaseURL is required")
	}
	if !strings.HasPrefix(c.APIBaseURL, "http://") && !strings.HasPrefix(c.APIBaseURL, "https://") {
		return fmt.Errorf("apiBaseURL must start with http:// or https://, got %q", c.APIBaseURL)
	}
	if c.Token == "" {
		return fmt.Errorf("token is required")
	}
	if c.ProjectID <= 0 {
		return fmt.Errorf("projectID must be > 0, got %d", c.ProjectID)
	}
	if c.PageSize < minPageSize {
		return fmt.Errorf("pageSize must be >= %d (TA OpenAPI minimum), got %d", minPageSize, c.PageSize)
	}
	if c.Table == TableEvent {
		if _, err := time.Parse("2006-01-02", c.PartDateRange.Start); err != nil {
			return fmt.Errorf("partDateRange.start must be YYYY-MM-DD: %w", err)
		}
		if _, err := time.Parse("2006-01-02", c.PartDateRange.End); err != nil {
			return fmt.Errorf("partDateRange.end must be YYYY-MM-DD: %w", err)
		}
	}
	if c.Proxy != "" {
		u, err := url.Parse(c.Proxy)
		if err != nil {
			return fmt.Errorf("proxy is not a valid URL: %w", err)
		}
		switch u.Scheme {
		case "http", "https", "socks5":
		default:
			return fmt.Errorf("proxy scheme %q unsupported (want http/https/socks5)", u.Scheme)
		}
	}
	if !c.EventTimeRange.Empty() {
		if c.EventTimeRange.Start != "" {
			if _, err := time.Parse("2006-01-02 15:04:05", c.EventTimeRange.Start); err != nil {
				return fmt.Errorf("eventTimeRange.start must be YYYY-MM-DD HH:MM:SS: %w", err)
			}
		}
		if c.EventTimeRange.End != "" {
			if _, err := time.Parse("2006-01-02 15:04:05", c.EventTimeRange.End); err != nil {
				return fmt.Errorf("eventTimeRange.end must be YYYY-MM-DD HH:MM:SS: %w", err)
			}
		}
	}
	return nil
}

// ForceSkip reports whether the user table writes use $setOnInsert (user_setOnce
// → never overwrite live state). Defaults to true when unset.
func (c *Config) ForceSkip() bool {
	return c.ForceSkipExisting == nil || *c.ForceSkipExisting
}

// userType is the #type injected for user-table rows: user_setOnce when
// ForceSkip (never overwrite) else user_set.
func (c *Config) userType() string {
	if c.ForceSkip() {
		return typeUserSetOnce
	}
	return typeUserSet
}

// defaultType is the #type injected for a fetched row that carries none: track
// for the event table, user_setOnce/user_set for the user table.
func (c *Config) defaultType() string {
	if c.Table == TableUser {
		return c.userType()
	}
	return typeEvent
}

// ShouldPaginate reports whether server-side pagination is requested. Defaults
// to true when unset.
func (c *Config) ShouldPaginate() bool {
	return c.Paginate == nil || *c.Paginate
}

// EffectivePageSize is the pageSize passed to /open/submit-sql: the configured
// PageSize when paginating, else 0 (no pagination → a single result page).
func (c *Config) EffectivePageSize() int {
	if c.ShouldPaginate() {
		return c.PageSize
	}
	return 0
}

// Days enumerates the fetch units: one per partition date in the inclusive
// [start, end] range for the event table, or a single UserChunkKey for the
// user table.
func (c *Config) Days() ([]string, error) {
	if c.Table == TableUser {
		return []string{UserChunkKey}, nil
	}
	startT, err := time.Parse("2006-01-02", c.PartDateRange.Start)
	if err != nil {
		return nil, fmt.Errorf("partDateRange.start: %w", err)
	}
	endT, err := time.Parse("2006-01-02", c.PartDateRange.End)
	if err != nil {
		return nil, fmt.Errorf("partDateRange.end: %w", err)
	}
	if endT.Before(startT) {
		return nil, fmt.Errorf("partDateRange.end %s is before start %s", c.PartDateRange.End, c.PartDateRange.Start)
	}
	var out []string
	for d := startT; !d.After(endT); d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format("2006-01-02"))
	}
	return out, nil
}

// BuildSQL renders the TA OpenAPI SQL for one partition date. For the event
// table it pins "$part_date", optional event-time bounds and an optional
// event-name IN-list; for the user table the date is ignored (the table is
// unpartitioned). There is NO include/exclude filter push-down — selectivity
// beyond event-name lives in the engine's reporting filter (parser.filter.*),
// so this module needs no dependency on parser/filter.
func (c *Config) BuildSQL(day string) string { return c.buildSelect(day, c.Limit) }

// BuildProbeSQL builds a 1-row query (same FROM/WHERE as BuildSQL, always
// LIMIT 1) used only to discover the result column headers via the synchronous
// querySql fallback when the async sql-task-info response omits them (TA drops
// headers for very wide SELECT * results, e.g. the ~985-column event view).
func (c *Config) BuildProbeSQL(day string) string { return c.buildSelect(day, 1) }

func (c *Config) buildSelect(day string, limit int) string {
	var b strings.Builder
	b.WriteString(`SELECT * FROM `)
	if c.SchemaPrefix != "" {
		fmt.Fprintf(&b, "%s.", c.SchemaPrefix)
	}
	if c.Table == TableUser {
		fmt.Fprintf(&b, "v_user_%d", c.ProjectID)
	} else {
		fmt.Fprintf(&b, "v_event_%d", c.ProjectID)
	}

	var predicates []string
	if c.Table == TableEvent {
		predicates = append(predicates, fmt.Sprintf(`"$part_date" = '%s'`, day))
		if s := c.EventTimeRange.Start; s != "" {
			predicates = append(predicates, fmt.Sprintf(`"#event_time" >= '%s'`, s))
		}
		if e := c.EventTimeRange.End; e != "" {
			predicates = append(predicates, fmt.Sprintf(`"#event_time" <= '%s'`, e))
		}
		if len(c.Events) > 0 {
			quoted := make([]string, 0, len(c.Events))
			for _, ev := range c.Events {
				quoted = append(quoted, "'"+strings.ReplaceAll(ev, "'", "''")+"'")
			}
			predicates = append(predicates, fmt.Sprintf(`"#event_name" IN (%s)`, strings.Join(quoted, ", ")))
		}
	}
	// UserWhere is the user-table shard predicate (the table is unpartitioned, so
	// distributed slicing is expressed as a WHERE). Raw, trusted operator SQL.
	if c.Table == TableUser && c.UserWhere != "" {
		predicates = append(predicates, c.UserWhere)
	}
	if len(predicates) > 0 {
		fmt.Fprintf(&b, " WHERE %s", strings.Join(predicates, " AND "))
	}
	// ORDER BY is supported for the user table only (the event table is fetched
	// per partition day). It lets a Limit select a deterministic slice — e.g.
	// the N most-recently-logged-in users via "last_login_time DESC".
	if c.Table == TableUser && c.UserOrderBy != "" {
		fmt.Fprintf(&b, " ORDER BY %s", c.UserOrderBy)
	}
	if limit > 0 {
		fmt.Fprintf(&b, " LIMIT %d", limit)
	}
	return b.String()
}
