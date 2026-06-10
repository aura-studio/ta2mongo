package cfgsync

import (
	"fmt"
	"time"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// Backend selectors (file key cfgsync.backend).
const (
	// BackendPoll polls the central config document on a fixed interval. It is the
	// default: it works against any MongoDB topology (including standalone) and on
	// DocumentDB without enabling change streams.
	BackendPoll = "poll"
	// BackendChangeStream subscribes to a change stream for low-latency pushes,
	// with a periodic full read as a safety net. It requires a replica set (plain
	// MongoDB) or change streams enabled (DocumentDB).
	BackendChangeStream = "changestream"
)

// Defaults for cfgsync.* keys.
const (
	// DefaultCollection is the central config collection. It is a fixed convention
	// (rarely overridden) and is the same collection the Publish side writes to.
	DefaultCollection = "_tango_config"
	// DefaultDocumentID is the _id of the config document cfgsync tracks.
	DefaultDocumentID = "filter"
	// DefaultPollInterval is the poll backend's read period: the worst-case
	// staleness window when running with backend=poll.
	DefaultPollInterval = 5 * time.Second
	// DefaultReconcileInterval is the changestream backend's fallback full-read
	// period — the self-healing safety net that bounds staleness even if the
	// stream drops events or breaks.
	DefaultReconcileInterval = 60 * time.Second
)

// FromTree decodes the cfgsync.* branch of t into a Config, applies defaults and
// validates it. Like the other domain modules cfgsync owns its own section name
// ("cfgsync"), so callers hand it the whole tree and it slices its own branch.
func FromTree(t cfgtree.Tree) (*Config, error) {
	var c Config
	if err := t.Sub("cfgsync").Into(&c); err != nil {
		return nil, fmt.Errorf("cfgsync: %w", err)
	}
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("cfgsync: %w", err)
	}
	return &c, nil
}

// Config is the cfgsync module's own configuration (file key cfgsync.*). It tunes
// the runtime dynamic-config Watcher: whether it runs, which backend drives it,
// which document it tracks, and the read cadences. cfgsync is intentionally
// disabled by default — turning on remote runtime reconfiguration is an explicit
// opt-in, not a surprise.
type Config struct {
	// Enabled turns the Watcher on (file key cfgsync.enabled). Default false.
	Enabled bool `mapstructure:"enabled"`
	// Backend selects the sync backend (file key cfgsync.backend): poll or
	// changestream. Default poll.
	Backend string `mapstructure:"backend"`
	// DocumentID is the _id of the tracked document in the config collection
	// (file key cfgsync.documentID). Default "filter".
	DocumentID string `mapstructure:"documentID"`
	// PollInterval is the poll backend's read period (file key
	// cfgsync.pollInterval). Default 5s.
	PollInterval time.Duration `mapstructure:"pollInterval"`
	// ReconcileInterval is the changestream backend's fallback full-read period
	// (file key cfgsync.reconcileInterval). Default 60s.
	ReconcileInterval time.Duration `mapstructure:"reconcileInterval"`
	// Collection is the config collection name (file key cfgsync.collection).
	// Default "_tango_config"; rarely changed.
	Collection string `mapstructure:"collection"`
}

// ApplyDefaults fills unset cfgsync options with their defaults.
func (c *Config) ApplyDefaults() {
	if c.Backend == "" {
		c.Backend = BackendPoll
	}
	if c.DocumentID == "" {
		c.DocumentID = DefaultDocumentID
	}
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultPollInterval
	}
	if c.ReconcileInterval <= 0 {
		c.ReconcileInterval = DefaultReconcileInterval
	}
	if c.Collection == "" {
		c.Collection = DefaultCollection
	}
}

// Validate checks the backend selector. The intervals are guarded by
// ApplyDefaults (non-positive values are reset to the default), so the only
// genuinely invalid input is an unknown backend.
func (c *Config) Validate() error {
	switch c.Backend {
	case BackendPoll, BackendChangeStream:
		return nil
	default:
		return fmt.Errorf("backend %q is invalid (want %q or %q)",
			c.Backend, BackendPoll, BackendChangeStream)
	}
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding and CLI flags know every key. It owns its leaf
// keys; the parent owns the prefix.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".enabled", false)
	set(prefix+".backend", "")
	set(prefix+".documentID", "")
	set(prefix+".pollInterval", "0s")
	set(prefix+".reconcileInterval", "0s")
	set(prefix+".collection", "")
}
