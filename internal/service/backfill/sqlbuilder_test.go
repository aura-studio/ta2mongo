package backfill

import (
	"testing"

	"rocket-nano/tools/tango/config"
)

// TestBuildDaySQL pins the TA SQL generated for a backfill day across the
// event/user tables, schema prefix, event-time range, filter pushdown, and
// limit. buildDaySQL only reads r.cfg, so a bare Runner suffices.
func TestBuildDaySQL(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		day  string
		want string
	}{
		{
			name: "event minimal",
			cfg: config.Config{
				Backfill:       config.BackfillConfig{ProjectID: 35},
				BackfillFilter: config.BackfillFilterConfig{Table: config.BackfillTableEvent},
			},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01'`,
		},
		{
			name: "event with schema prefix",
			cfg: config.Config{
				Backfill:       config.BackfillConfig{ProjectID: 35, SchemaPrefix: "ta"},
				BackfillFilter: config.BackfillFilterConfig{Table: config.BackfillTableEvent},
			},
			day:  "2026-05-01",
			want: `SELECT * FROM ta.v_event_35 WHERE "$part_date" = '2026-05-01'`,
		},
		{
			name: "event with event-time range and limit",
			cfg: config.Config{
				Backfill: config.BackfillConfig{
					ProjectID:      35,
					EventTimeRange: config.TimeRange{Start: "2026-05-01 00:00:00", End: "2026-05-01 12:00:00"},
					Limit:          100,
				},
				BackfillFilter: config.BackfillFilterConfig{Table: config.BackfillTableEvent},
			},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01' AND "#event_time" >= '2026-05-01 00:00:00' AND "#event_time" <= '2026-05-01 12:00:00' LIMIT 100`,
		},
		{
			name: "event with events filter pushdown",
			cfg: config.Config{
				Backfill: config.BackfillConfig{ProjectID: 35},
				BackfillFilter: config.BackfillFilterConfig{
					Table:  config.BackfillTableEvent,
					Events: []string{"login", "pay"},
				},
			},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01' AND ("#event_name" IN ('login', 'pay'))`,
		},
		{
			name: "user table has no part_date",
			cfg: config.Config{
				Backfill:       config.BackfillConfig{ProjectID: 35},
				BackfillFilter: config.BackfillFilterConfig{Table: config.BackfillTableUser},
			},
			day:  "2026-05-01",
			want: `SELECT * FROM v_user_35`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := &Runner{cfg: c.cfg}
			if got := r.buildDaySQL(c.day); got != c.want {
				t.Errorf("buildDaySQL()\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}
