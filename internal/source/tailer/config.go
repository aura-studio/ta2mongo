package tailer

import (
	"fmt"
	"time"
)

// TailMode constants control how the tailer watches for file changes.
const (
	// TailModeHybrid uses hpcloud/tail's event-driven watcher as the primary
	// mechanism, with a periodic poll fallback that detects missed
	// notifications. Combines low latency with reliability.
	TailModeHybrid = "hybrid"

	// TailModePoll uses a simple polling loop (read → sleep → retry). Immune to
	// notification-drop races; suitable for all workloads.
	TailModePoll = "poll"

	// TailModeEvent uses hpcloud/tail's kqueue/inotify event-driven watcher.
	// Lowest latency but may stall under sustained concurrent writes due to a
	// known sendOnlyIfEmpty race in the upstream library.
	TailModeEvent = "event"
)

// Config configures the file-tailing data source (the report daemon's source).
// It is the tailer module's own configuration; the top-level config package
// references it by pointer and the loader unmarshals the source.* keys into it.
type Config struct {
	// LogPattern is a list of glob/regex patterns matched against file paths.
	// Required for the report service.
	LogPattern []string `mapstructure:"logPattern"`
	// TailMode selects the file-tailing strategy: hybrid (default) / poll / event.
	TailMode string `mapstructure:"tailMode"`
	// RescanInterval is how often the tailer rescans for new files. Default 30s.
	RescanInterval time.Duration `mapstructure:"rescanInterval"`
	// PollInterval is the poll cadence used by poll / hybrid tail modes.
	// Default 200ms.
	PollInterval time.Duration `mapstructure:"pollInterval"`
	// MaxLineBytes caps a single log line's length. Default 10485760 (10 MB).
	MaxLineBytes int `mapstructure:"maxLineBytes"`
}

// Validate checks the tail mode is recognised (empty = use default). The log
// pattern is not required here — an empty pattern is a valid tailer config that
// simply matches nothing; roles that require it (daemon) check separately.
func (c *Config) Validate() error {
	switch c.TailMode {
	case "", TailModeHybrid, TailModePoll, TailModeEvent:
		return nil
	default:
		return fmt.Errorf("tailMode must be %q/%q/%q, got %q",
			TailModeHybrid, TailModePoll, TailModeEvent, c.TailMode)
	}
}

// RegisterDefaults registers this module's config keys (under prefix) with the
// given setter so env binding works.
func (c *Config) RegisterDefaults(set func(key string, value any), prefix string) {
	set(prefix+".logPattern", []string{})
	set(prefix+".tailMode", "")
	set(prefix+".rescanInterval", "0s")
	set(prefix+".pollInterval", "0s")
	set(prefix+".maxLineBytes", 0)
}

// ApplyDefaults fills unset tailer options.
func (c *Config) ApplyDefaults() {
	if c.RescanInterval <= 0 {
		c.RescanInterval = 30 * time.Second
	}
	if c.TailMode == "" {
		c.TailMode = TailModeHybrid
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 200 * time.Millisecond
	}
	if c.MaxLineBytes <= 0 {
		c.MaxLineBytes = 10 * 1024 * 1024
	}
}
