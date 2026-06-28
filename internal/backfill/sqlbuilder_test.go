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
		{
			name: "user table ordered by recency (top-N)",
			cfg:  &Config{ProjectID: 35, Table: TableUser, UserOrderBy: "last_login_time DESC", Limit: 10000},
			day:  "ignored",
			want: `SELECT * FROM v_user_35 ORDER BY last_login_time DESC LIMIT 10000`,
		},
		{
			name: "userOrderBy ignored for event table",
			cfg:  &Config{ProjectID: 35, Table: TableEvent, UserOrderBy: "x DESC", PartDateRange: DateRange{Start: "2026-05-01", End: "2026-05-01"}},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01'`,
		},
		{
			name: "user table hash shard (userWhere)",
			cfg:  &Config{ProjectID: 35, Table: TableUser, UserWhere: `mod(cast("#user_id" AS bigint) / 4194304, 8) = 3`},
			day:  "ignored",
			want: `SELECT * FROM v_user_35 WHERE mod(cast("#user_id" AS bigint) / 4194304, 8) = 3`,
		},
		{
			name: "user table shard + orderBy + limit",
			cfg:  &Config{ProjectID: 35, Table: TableUser, UserWhere: `mod(cast("#user_id" AS bigint) / 4194304, 4) = 1`, UserOrderBy: "last_login_time DESC", Limit: 5000},
			day:  "ignored",
			want: `SELECT * FROM v_user_35 WHERE mod(cast("#user_id" AS bigint) / 4194304, 4) = 1 ORDER BY last_login_time DESC LIMIT 5000`,
		},
		{
			name: "userWhere ignored for event table",
			cfg:  &Config{ProjectID: 35, Table: TableEvent, UserWhere: "x = 1", PartDateRange: DateRange{Start: "2026-05-01", End: "2026-05-01"}},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01'`,
		},
		{
			name: "event sub-shard (eventWhere) with events",
			cfg:  &Config{ProjectID: 35, Table: TableEvent, Events: []string{"pay"}, EventWhere: `mod(from_base(substr("#uuid", 1, 8), 16), 8) = 3`},
			day:  "2026-05-01",
			want: `SELECT * FROM v_event_35 WHERE "$part_date" = '2026-05-01' AND "#event_name" IN ('pay') AND mod(from_base(substr("#uuid", 1, 8), 16), 8) = 3`,
		},
		{
			name: "eventWhere ignored for user table",
			cfg:  &Config{ProjectID: 35, Table: TableUser, EventWhere: "x = 1"},
			day:  "ignored",
			want: `SELECT * FROM v_user_35`,
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
