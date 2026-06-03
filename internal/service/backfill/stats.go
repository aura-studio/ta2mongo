package backfill

import "sync/atomic"

// Stats records counters for the backfill run. Mirrors pipeline.StatsCollector
// behaviour and adds backfill-specific counters (HTTP errors, pages fetched).
type Stats struct {
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
	Pages          atomic.Int64
	HTTPErrors     atomic.Int64
	DaysCompleted  atomic.Int64
	DaysFailed     atomic.Int64
}

// statsCollector adapts Stats to pipeline.StatsCollector.
type statsCollector struct{ s *Stats }

func (c *statsCollector) OnLine()          { c.s.TotalLines.Add(1) }
func (c *statsCollector) OnParseOK()       { c.s.ParsedOK.Add(1) }
func (c *statsCollector) OnParseError()    { c.s.ParseErrors.Add(1) }
func (c *statsCollector) OnIdentityError() { c.s.IdentityErrors.Add(1) }
func (c *statsCollector) OnUserWrite()     { c.s.UserWrites.Add(1) }
func (c *statsCollector) OnEventWrite()    { c.s.EventWrites.Add(1) }
func (c *statsCollector) OnDeadLetter()    { c.s.DeadLetters.Add(1) }
func (c *statsCollector) OnWriteError()    { c.s.WriteErrors.Add(1) }
func (c *statsCollector) OnFiltered()      { c.s.Filtered.Add(1) }
func (c *statsCollector) OnFilterError()   { c.s.FilterErrors.Add(1) }
