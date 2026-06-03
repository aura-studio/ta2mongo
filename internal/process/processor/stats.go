// Package processor holds the shared per-line ingestion semantics — parse,
// filter, identity resolve, and write-model construction — used by the async
// pipeline, the synchronous ingest API, and backfill's event path, so the
// rules (what becomes a user/event write, what is filtered, what goes to dead
// letter) live in exactly one place.
package processor

// StatsCollector is an optional callback interface for recording processing
// statistics. Implementations must be safe for concurrent use.
type StatsCollector interface {
	// OnLine is called for every line received.
	OnLine()
	// OnParseOK is called when a line is successfully parsed.
	OnParseOK()
	// OnParseError is called when a line fails to parse.
	OnParseError()
	// OnIdentityError is called when identity resolution fails.
	OnIdentityError()
	// OnUserWrite is called when a user write model is enqueued.
	OnUserWrite()
	// OnEventWrite is called when an event write model is enqueued.
	OnEventWrite()
	// OnDeadLetter is called when a line is sent to dead letter.
	OnDeadLetter()
	// OnWriteError is called when a bulk write fails.
	OnWriteError()
	// OnFiltered is called when a parsed record is dropped by filter rules.
	OnFiltered()
	// OnFilterError is called when a filter expression evaluation fails.
	OnFilterError()
}

// NoopStats is a StatsCollector that does nothing.
type NoopStats struct{}

func (NoopStats) OnLine()          {}
func (NoopStats) OnParseOK()       {}
func (NoopStats) OnParseError()    {}
func (NoopStats) OnIdentityError() {}
func (NoopStats) OnUserWrite()     {}
func (NoopStats) OnEventWrite()    {}
func (NoopStats) OnDeadLetter()    {}
func (NoopStats) OnWriteError()    {}
func (NoopStats) OnFiltered()      {}
func (NoopStats) OnFilterError()   {}
