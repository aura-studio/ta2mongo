package backfill

import "testing"

// TestBuildSQL pins the TA SQL generated for a backfill day across the
// event/user tables, schema prefix, event-time range, event-name IN-list and
// limit. There is no include/exclude filter push-down (that lives in the
// engine's reporting filter), so the SQL stays a plain SELECT.
func TestBuildSQL(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		day  string
		want string
	}{
		{
			name: "event minimal",
			cfg:  &Config{ProjectID: 35, Table: TableEvent},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01'`,
		},
		{
			name: "event with schema prefix",
			cfg:  &Config{ProjectID: 35, Table: TableEvent, SchemaPrefix: "ta"},
			day:  "2026-05-01",
			want: `SELECT * FROM ta.v_event_35 WHERE "$part_date" = '2026-05-01'`,
		},
		{
			name: "event with event-time range and limit",
			cfg: &Config{
				ProjectID:      35,
				Table:          TableEvent,
				EventTimeRange: TimeRange{Start: "2026-05-01 00:00:00", End: "2026-05-01 12:00:00"},
				Limit:          100,
			},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01' AND "#event_time" >= '2026-05-01 00:00:00' AND "#event_time" <= '2026-05-01 12:00:00' LIMIT 100`,
		},
		{
			name: "event with event-name IN-list",
			cfg:  &Config{ProjectID: 35, Table: TableEvent, Events: []string{"login", "pay"}},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01' AND "#event_name" IN ('login', 'pay')`,
		},
		{
			name: "user table has no part_date",
			cfg:  &Config{ProjectID: 35, Table: TableUser},
			day:  "2026-05-01",
			want: `SELECT * FROM v_user_35`,
		},
		{
			name: "user table with limit",
			cfg:  &Config{ProjectID: 35, Table: TableUser, Limit: 50},
			day:  "ignored",
			want: `SELECT * FROM v_user_35 LIMIT 50`,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.cfg.BuildSQL(c.day); got != c.want {
				t.Errorf("BuildSQL()\n got: %s\nwant: %s", got, c.want)
			}
		})
	}
}
