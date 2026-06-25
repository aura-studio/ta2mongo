package backfill

import "testing"

func validEventConfig() *Config {
	return &Config{
		APIBaseURL: "https://ta.example.com",
		Token:      "tok",
		ProjectID:  35,
		Table:      TableEvent,
		PartDateRange: DateRange{
			Start: "2026-05-01",
			End:   "2026-05-02",
		},
	}
}

func TestConfig_ApplyDefaults(t *testing.T) {
	c := &Config{}
	c.ApplyDefaults()
	if c.Table != TableEvent {
		t.Errorf("table default = %q, want %q", c.Table, TableEvent)
	}
	if c.PageSize != defaultPageSize {
		t.Errorf("pageSize default = %d, want %d", c.PageSize, defaultPageSize)
	}
	if c.PageRetries != 3 {
		t.Errorf("pageRetries default = %d, want 3", c.PageRetries)
	}
	if c.PollInterval <= 0 || c.PollTimeout <= 0 {
		t.Errorf("poll defaults not applied: interval=%v timeout=%v", c.PollInterval, c.PollTimeout)
	}
}

func TestConfig_Validate_OK(t *testing.T) {
	c := validEventConfig()
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		t.Fatalf("valid event config rejected: %v", err)
	}

	u := &Config{APIBaseURL: "https://ta.example.com", Token: "t", ProjectID: 1, Table: TableUser}
	u.ApplyDefaults()
	if err := u.Validate(); err != nil {
		t.Fatalf("valid user config rejected: %v", err)
	}
}

func TestConfig_Validate_Errors(t *testing.T) {
	cases := map[string]func(*Config){
		"missing apiBaseURL": func(c *Config) { c.APIBaseURL = "" },
		"bad apiBaseURL":     func(c *Config) { c.APIBaseURL = "ftp://x" },
		"missing token":      func(c *Config) { c.Token = "" },
		"bad projectID":      func(c *Config) { c.ProjectID = 0 },
		"bad table":          func(c *Config) { c.Table = "weird" },
		"small pageSize":     func(c *Config) { c.PageSize = 10 },
		"bad partDate":       func(c *Config) { c.PartDateRange.Start = "nope" },
		"bad proxy scheme":   func(c *Config) { c.Proxy = "ftp://p" },
		"bad eventTime":      func(c *Config) { c.EventTimeRange.Start = "2026-05-01" }, // missing time part
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validEventConfig()
			c.ApplyDefaults()
			mutate(c)
			if err := c.Validate(); err == nil {
				t.Errorf("%s: expected validation error", name)
			}
		})
	}
}

func TestConfig_Helpers(t *testing.T) {
	c := validEventConfig()
	c.ApplyDefaults()

	// ForceSkip / ShouldPaginate default to true.
	if !c.ForceSkip() {
		t.Error("ForceSkip default should be true")
	}
	if !c.ShouldPaginate() {
		t.Error("ShouldPaginate default should be true")
	}
	if c.EffectivePageSize() != defaultPageSize {
		t.Errorf("EffectivePageSize = %d, want %d", c.EffectivePageSize(), defaultPageSize)
	}
	// Event table injects #type=track.
	if got := c.defaultType(); got != typeEvent {
		t.Errorf("event defaultType = %q, want %q", got, typeEvent)
	}

	// Explicit false overrides.
	f := false
	c.ForceSkipExisting = &f
	c.Paginate = &f
	if c.ForceSkip() {
		t.Error("ForceSkip should honour explicit false")
	}
	if c.ShouldPaginate() {
		t.Error("ShouldPaginate should honour explicit false")
	}
	if c.EffectivePageSize() != 0 {
		t.Errorf("EffectivePageSize with paginate=false = %d, want 0", c.EffectivePageSize())
	}

	// User table injects user_setOnce by default, user_set when ForceSkip=false.
	u := &Config{Table: TableUser}
	if got := u.defaultType(); got != typeUserSetOnce {
		t.Errorf("user defaultType (default) = %q, want %q", got, typeUserSetOnce)
	}
	u.ForceSkipExisting = &f
	if got := u.defaultType(); got != typeUserSet {
		t.Errorf("user defaultType (force=false) = %q, want %q", got, typeUserSet)
	}
}

func TestConfig_Days(t *testing.T) {
	c := &Config{Table: TableEvent, PartDateRange: DateRange{Start: "2026-05-01", End: "2026-05-03"}}
	days, err := c.Days()
	if err != nil {
		t.Fatalf("Days: %v", err)
	}
	want := []string{"2026-05-01", "2026-05-02", "2026-05-03"}
	if len(days) != len(want) {
		t.Fatalf("Days = %v, want %v", days, want)
	}
	for i := range want {
		if days[i] != want[i] {
			t.Errorf("Days[%d] = %q, want %q", i, days[i], want[i])
		}
	}

	u := &Config{Table: TableUser}
	udays, err := u.Days()
	if err != nil {
		t.Fatalf("user Days: %v", err)
	}
	if len(udays) != 1 || udays[0] != UserChunkKey {
		t.Errorf("user Days = %v, want [%s]", udays, UserChunkKey)
	}
}
