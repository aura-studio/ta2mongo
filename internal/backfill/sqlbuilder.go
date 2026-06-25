package backfill

import (
	"fmt"
	"strings"
)

// buildDaySQL renders the per-day TA OpenAPI SQL for a single partition date.
// Pagination is server-side (driven by EffectivePageSize passed to submit), so
// the SQL carries no page/offset — only the partition pin, optional event-time
// bounds, the pushed-down selection filter, and an optional row Limit.
//
//	SELECT * FROM [schema.]v_event_<pid> WHERE "$part_date" = '<day>'
//	    [AND "#event_time" >= '<start>'] [AND "#event_time" <= '<end>']
//	    [AND <filterWhere>] [LIMIT <n>]
//
// For the user table the SELECT pulls v_user_<pid> with no partition / no
// event-time predicate (day is ignored — user tables run as one task).
func (r *Runner) buildDaySQL(day string) string {
	var b strings.Builder
	b.WriteString(`SELECT * FROM `)
	if schema := r.cfg.SchemaPrefix; schema != "" {
		fmt.Fprintf(&b, "%s.", schema)
	}
	switch r.cfg.Table {
	case TableUser:
		fmt.Fprintf(&b, "v_user_%d", r.cfg.ProjectID)
	default:
		fmt.Fprintf(&b, "v_event_%d", r.cfg.ProjectID)
	}

	predicates := []string{}
	if r.cfg.Table == TableEvent {
		predicates = append(predicates, fmt.Sprintf(`"$part_date" = '%s'`, day))
		if start := r.cfg.EventTimeRange.Start; start != "" {
			predicates = append(predicates, fmt.Sprintf(`"#event_time" >= '%s'`, start))
		}
		if end := r.cfg.EventTimeRange.End; end != "" {
			predicates = append(predicates, fmt.Sprintf(`"#event_time" <= '%s'`, end))
		}
	}
	// Filter pushdown (both tables). The error is intentionally swallowed —
	// validity is established at config-validate time (Config.Validate calls
	// BackfillWhere).
	if filterWhere, _ := r.cfg.BackfillWhere(); filterWhere != "" {
		predicates = append(predicates, filterWhere)
	}
	if len(predicates) > 0 {
		fmt.Fprintf(&b, " WHERE %s", strings.Join(predicates, " AND "))
	}
	if lim := r.cfg.Limit; lim > 0 {
		fmt.Fprintf(&b, " LIMIT %d", lim)
	}
	return b.String()
}
