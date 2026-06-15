package backfill

import (
	"sync/atomic"

	"github.com/aura-studio/tango/internal/process"
)

// Stats records counters for a backfill run. It embeds the shared per-line
// ingestion Counters (the ten metrics, also making Stats a StatsCollector) and
// adds backfill-specific counters.
type Stats struct {
	process.Counters
	Pages         atomic.Int64
	HTTPErrors    atomic.Int64
	DaysCompleted atomic.Int64
	DaysFailed    atomic.Int64
}
