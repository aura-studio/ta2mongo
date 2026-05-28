package backfill

import (
	"testing"
)

func TestInitDays_Range(t *testing.T) {
	days, err := initDays("2026-01-30", "2026-02-02")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2026-01-30", "2026-01-31", "2026-02-01", "2026-02-02"}
	if len(days) != len(want) {
		t.Fatalf("len = %d, want %d", len(days), len(want))
	}
	for _, d := range want {
		p, ok := days[d]
		if !ok {
			t.Errorf("missing day %s", d)
		}
		if p.Status != DayPending {
			t.Errorf("day %s status = %q", d, p.Status)
		}
		if p.PageID != -1 {
			t.Errorf("day %s initial pageID = %d, want -1", d, p.PageID)
		}
	}
}

func TestInitDays_EndBeforeStart(t *testing.T) {
	_, err := initDays("2026-02-02", "2026-01-30")
	if err == nil {
		t.Fatal("expected error for reversed range")
	}
}

func TestInitDays_BadFormat(t *testing.T) {
	_, err := initDays("not-a-date", "2026-01-01")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSQLSignature_Stable(t *testing.T) {
	s1 := SQLSignature("event", 102, `"#type" = 'track'`, "", "")
	s2 := SQLSignature("event", 102, `"#type" = 'track'`, "", "")
	if s1 != s2 {
		t.Errorf("not deterministic: %s vs %s", s1, s2)
	}
}

func TestSQLSignature_DifferentOnChange(t *testing.T) {
	base := SQLSignature("event", 102, `"#type" = 'track'`, "", "")
	cases := map[string]string{
		"table":       SQLSignature("user", 102, `"#type" = 'track'`, "", ""),
		"project":    SQLSignature("event", 999, `"#type" = 'track'`, "", ""),
		"filter":     SQLSignature("event", 102, `"#type" = 'user_set'`, "", ""),
		"event_time": SQLSignature("event", 102, `"#type" = 'track'`, "2026-01-01 00:00:00", ""),
	}
	for name, sig := range cases {
		if sig == base {
			t.Errorf("%s: signature did not change", name)
		}
	}
}
