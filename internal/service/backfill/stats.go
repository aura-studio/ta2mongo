package backfill

import (
	"sync/atomic"

	"rocket-nano/tools/tango/internal/process/ingestion"
)

// Stats records counters for the backfill run. It embeds the shared ingestion
// Counters (the ten per-line metrics, which also makes Stats a
// StatsCollector) and adds backfill-specific counters.
type Stats struct {
	ingestion.Counters
	Pages         atomic.Int64
	HTTPErrors    atomic.Int64
	DaysCompleted atomic.Int64
	DaysFailed    atomic.Int64
}
