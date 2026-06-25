package backfill

import "testing"

func validEventConfig() *Config {
	return &Config{
		APIBaseURL: "https://ta.example.com",
		Token:      "tok",
		ProjectID:  35,
		Table:      TableEvent,
		RunID:      "run-1",
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
	if c.ProgressCollection != DefaultProgressCollection {
		t.Errorf("progressCollection default = %q, want %q", c.ProgressCollection, DefaultProgressCollection)
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

	u := &Config{APIBaseURL: "https://ta.example.com", Token: "t", ProjectID: 1, Table: TableUser, RunID: "r"}
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
		"missing runID":      func(c *Config) { c.RunID = "" },
		"bad table":          func(c *Config) { c.Table = "weird" },
		"small pageSize":     func(c *Config) { c.PageSize = 10 },
		"bad partDate":       func(c *Config) { c.PartDateRange.Start = "nope" },
		"bad proxy scheme":   func(c *Config) { c.Proxy = "ftp://p" },
		"bad eventTime":      func(c *Config) { c.EventTimeRange.Start = "2026-05-01" }, // missing time part
		"bad filter expr":    func(c *Config) { c.Include = []string{"func("} },
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
}

func TestConfig_IncludeExprs_And_Where(t *testing.T) {
	c := &Config{Table: TableEvent, Events: []string{"login", "pay"}, Include: []string{`#type == "track"`}}
	exprs := c.IncludeExprs()
	if len(exprs) != 2 {
		t.Fatalf("IncludeExprs = %v, want include + derived event-name", exprs)
	}

	where, err := c.BackfillWhere()
	if err != nil {
		t.Fatalf("BackfillWhere: %v", err)
	}
	// Both the explicit include and the derived event-name predicate must
	// appear, joined as a single OR include group.
	if want := `("#event_name" IN ('login', 'pay'))`; !contains(where, want) {
		t.Errorf("BackfillWhere %q missing event-name pushdown %q", where, want)
	}
	if !contains(where, `"#type" = 'track'`) {
		t.Errorf("BackfillWhere %q missing #type pushdown", where)
	}

	// User table drops the event-name derivation.
	cu := &Config{Table: TableUser, Events: []string{"login"}}
	if len(cu.IncludeExprs()) != 0 {
		t.Errorf("user table IncludeExprs = %v, want empty (no event derivation)", cu.IncludeExprs())
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOfStr(haystack, needle) >= 0
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
