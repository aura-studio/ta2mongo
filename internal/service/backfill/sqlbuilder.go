package backfill

import (
	"fmt"
	"strings"

	"rocket-nano/tools/tango/config"
)

// buildDaySQL constructs the Presto SQL for one chunk of work.
//
// For the event table (`v_event_<pid>`), chunks are partition-dates and the
// SQL pins `"$part_date" = '<day>'`. For the user table (`v_user_<pid>`)
// there is no partition column, so the day argument is ignored and the
// SELECT pulls the whole table in one task. Optional eventTimeRange bounds
// (event table only) and the pushed-down filter WHERE are appended.
func (r *Runner) buildDaySQL(day string) string {
	var b strings.Builder
	b.WriteString(`SELECT * FROM `)
	if schema := r.cfg.Backfill.SchemaPrefix; schema != "" {
		fmt.Fprintf(&b, "%s.", schema)
	}
	switch r.cfg.BackfillFilter.Table {
	case config.BackfillTableUser:
		fmt.Fprintf(&b, "v_user_%d", r.cfg.Backfill.ProjectID)
	default:
		fmt.Fprintf(&b, "v_event_%d", r.cfg.Backfill.ProjectID)
	}

	predicates := []string{}
	if r.cfg.BackfillFilter.Table == config.BackfillTableEvent {
		predicates = append(predicates, fmt.Sprintf(`"$part_date" = '%s'`, day))
		if start := r.cfg.Backfill.EventTimeRange.Start; start != "" {
			predicates = append(predicates, fmt.Sprintf(`"#event_time" >= '%s'`, start))
		}
		if end := r.cfg.Backfill.EventTimeRange.End; end != "" {
			predicates = append(predicates, fmt.Sprintf(`"#event_time" <= '%s'`, end))
		}
	}
	if filterWhere, _ := r.cfg.BackfillWhere(); filterWhere != "" {
		predicates = append(predicates, filterWhere)
	}
	if len(predicates) > 0 {
		fmt.Fprintf(&b, " WHERE %s", strings.Join(predicates, " AND "))
	}
	if lim := r.cfg.Backfill.Limit; lim > 0 {
		fmt.Fprintf(&b, " LIMIT %d", lim)
	}
	return b.String()
}
