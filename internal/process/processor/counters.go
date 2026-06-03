package processor

import "sync/atomic"

// Counters is the canonical concurrent StatsCollector implementation shared by
// the report service and backfill. It records the ten per-line ingestion
// metrics; callers that need extra counters embed it (see backfill.Stats).
type Counters struct {
	TotalLines     atomic.Int64
	ParsedOK       atomic.Int64
	ParseErrors    atomic.Int64
	IdentityErrors atomic.Int64
	UserWrites     atomic.Int64
	EventWrites    atomic.Int64
	DeadLetters    atomic.Int64
	WriteErrors    atomic.Int64
	Filtered       atomic.Int64
	FilterErrors   atomic.Int64
}

func (c *Counters) OnLine()          { c.TotalLines.Add(1) }
func (c *Counters) OnParseOK()       { c.ParsedOK.Add(1) }
func (c *Counters) OnParseError()    { c.ParseErrors.Add(1) }
func (c *Counters) OnIdentityError() { c.IdentityErrors.Add(1) }
func (c *Counters) OnUserWrite()     { c.UserWrites.Add(1) }
func (c *Counters) OnEventWrite()    { c.EventWrites.Add(1) }
func (c *Counters) OnDeadLetter()    { c.DeadLetters.Add(1) }
func (c *Counters) OnWriteError()    { c.WriteErrors.Add(1) }
func (c *Counters) OnFiltered()      { c.Filtered.Add(1) }
func (c *Counters) OnFilterError()   { c.FilterErrors.Add(1) }

// Snapshot is a point-in-time copy of the counter values for reporting.
type Snapshot struct {
	TotalLines     int64
	ParsedOK       int64
	ParseErrors    int64
	IdentityErrors int64
	UserWrites     int64
	EventWrites    int64
	DeadLetters    int64
	WriteErrors    int64
	Filtered       int64
	FilterErrors   int64
}

// Snapshot reads all counters into a Snapshot.
func (c *Counters) Snapshot() Snapshot {
	return Snapshot{
		TotalLines:     c.TotalLines.Load(),
		ParsedOK:       c.ParsedOK.Load(),
		ParseErrors:    c.ParseErrors.Load(),
		IdentityErrors: c.IdentityErrors.Load(),
		UserWrites:     c.UserWrites.Load(),
		EventWrites:    c.EventWrites.Load(),
		DeadLetters:    c.DeadLetters.Load(),
		WriteErrors:    c.WriteErrors.Load(),
		Filtered:       c.Filtered.Load(),
		FilterErrors:   c.FilterErrors.Load(),
	}
}
