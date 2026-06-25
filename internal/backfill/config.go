package backfill

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/parser/filter"
)

// Table identifiers and defaults for the backfill domain.
const (
	// TableEvent selects the partitioned event table v_event_<projectID>.
	TableEvent = "event"
	// TableUser selects the unpartitioned user-state table v_user_<projectID>.
	TableUser = "user"
	// DefaultProgressCollection is the Mongo collection that holds per-run
	// checkpoint documents.
	DefaultProgressCollection = "_backfill_progress"
	// minPageSize is the TA OpenAPI's minimum server-side page size.
	minPageSize = 1000
	// defaultPageSize is the page size used when none is configured.
	defaultPageSize = 10000
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

// Config is the backfill domain's own configuration (file key backfill.*). It
// folds the v1.0 BackfillConfig and BackfillFilterConfig into one module config
// following the per-module convention (FromTree / RegisterDefaults /
// ApplyDefaults / Validate). It is consumed by Engine.RunBackfill and cli
// function=backfill; the client SDK reaches it through the api.BackfillConfig
// alias.
type Config struct {
	// --- TA OpenAPI connection ---
	APIBaseURL string `mapstructure:"apiBaseURL"`
	Token      string `mapstructure:"token"`
	Proxy      string `mapstructure:"proxy"`
	ProjectID  int    `mapstructure:"projectID"`

	// --- query selection ---
	Table          string    `mapstructure:"table"` // "event" (default) or "user"
	Events         []string  `mapstructure:"events"`
	Include        []string  `mapstructure:"include"`
	Exclude        []string  `mapstructure:"exclude"`
	SchemaPrefix   string    `mapstructure:"schemaPrefix"`
	PartDateRange  DateRange `mapstructure:"partDateRange"`
	EventTimeRange TimeRange `mapstructure:"eventTimeRange"`
	Limit          int       `mapstructure:"limit"`

	// --- pagination / polling ---
	PageSize     int           `mapstructure:"pageSize"`
	Paginate     *bool         `mapstructure:"paginate"` // nil/true = paginate
	PageRetries  int           `mapstructure:"pageRetries"`
	PollInterval time.Duration `mapstructure:"pollInterval"`
	PollTimeout  time.Duration `mapstructure:"pollTimeout"`

	// --- run identity / write semantics ---
	RunID              string `mapstructure:"runID"`
	ProgressCollection string `mapstructure:"progressCollection"`
	ForceSkipExisting  *bool  `mapstructure:"forceSkipExisting"` // nil/true = $setOnInsert
	SkipLocalFilter    bool   `mapstructure:"skipLocalFilter"`
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
// binding works. The *bool tri-state fields default to true (the safe value,
// equivalent to their nil semantics): paginate on, skip-existing on.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".apiBaseURL", "")
	set(prefix+".token", "")
	set(prefix+".proxy", "")
	set(prefix+".projectID", 0)
	set(prefix+".table", "")
	set(prefix+".events", []string{})
	set(prefix+".include", []string{})
	set(prefix+".exclude", []string{})
	set(prefix+".schemaPrefix", "")
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
	set(prefix+".runID", "")
	set(prefix+".progressCollection", "")
	set(prefix+".forceSkipExisting", true)
	set(prefix+".skipLocalFilter", false)
}

// ApplyDefaults fills unset backfill options.
func (c *Config) ApplyDefaults() {
	if c.Table == "" {
		c.Table = TableEvent
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
	if c.ProgressCollection == "" {
		c.ProgressCollection = DefaultProgressCollection
	}
}

// Validate checks the backfill config. It is exported (no longer takes the
// table as a parameter, since table lives in the same struct now).
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
	if c.RunID == "" {
		return fmt.Errorf("runID is required (used as resume key)")
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
	// The selection filter must compile both to a local filter and to SQL.
	if _, err := filter.New(c.IncludeExprs(), c.Exclude); err != nil {
		return fmt.Errorf("filter does not compile: %w", err)
	}
	if _, err := c.BackfillWhere(); err != nil {
		return fmt.Errorf("filter does not compile to SQL pushdown: %w", err)
	}
	return nil
}

// ForceSkip reports whether historical writes use $setOnInsert (skip existing).
// Defaults to true when unset, so backfill never overwrites live data.
func (c *Config) ForceSkip() bool {
	if c.ForceSkipExisting == nil {
		return true
	}
	return *c.ForceSkipExisting
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

// IncludeExprs returns the include expression list with the event-name filter
// derived from Events appended (event table only).
func (c *Config) IncludeExprs() []string {
	out := append([]string(nil), c.Include...)
	if c.Table != TableUser && len(c.Events) > 0 {
		quoted := make([]string, 0, len(c.Events))
		for _, e := range c.Events {
			quoted = append(quoted, strconv.Quote(e))
		}
		out = append(out, `#event_name in [`+strings.Join(quoted, ", ")+`]`)
	}
	return out
}

// BackfillWhere renders the selection filter as a Presto WHERE-clause body (no
// leading WHERE), pushing the predicate down to the TA OpenAPI.
func (c *Config) BackfillWhere() (string, error) {
	return filter.CompileToSQL(c.IncludeExprs(), c.Exclude)
}

// BuildFilter compiles the in-process safety-net filter used by the user-table
// path behind the SQL pushdown.
func (c *Config) BuildFilter() (*filter.Filter, error) {
	return filter.New(c.IncludeExprs(), c.Exclude)
}
