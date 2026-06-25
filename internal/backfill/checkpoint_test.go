package backfill

import "testing"

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
	if _, err := initDays("2026-02-02", "2026-01-30"); err == nil {
		t.Fatal("expected error for reversed range")
	}
}

func TestInitDays_BadFormat(t *testing.T) {
	if _, err := initDays("not-a-date", "2026-01-01"); err == nil {
		t.Fatal("expected error")
	}
}

func TestInitChunks_UserChunkKey(t *testing.T) {
	chunks, err := initChunks(UserChunkKey, UserChunkKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 {
		t.Fatalf("user run should have one chunk, got %d", len(chunks))
	}
	p, ok := chunks[UserChunkKey]
	if !ok || p.Status != DayPending || p.PageID != -1 {
		t.Errorf("user chunk = %+v, want {pending, pageID=-1}", p)
	}
}

func TestSQLSignature_Stable(t *testing.T) {
	s1 := SQLSignature("event", 102, `"#type" = 'track'`, "", "")
	s2 := SQLSignature("event", 102, `"#type" = 'track'`, "", "")
	if s1 != s2 {
		t.Errorf("not deterministic: %s vs %s", s1, s2)
	}
	if len(s1) != 16 {
		t.Errorf("signature length = %d, want 16", len(s1))
	}
}

func TestSQLSignature_DifferentOnChange(t *testing.T) {
	base := SQLSignature("event", 102, `"#type" = 'track'`, "", "")
	cases := map[string]string{
		"table":      SQLSignature("user", 102, `"#type" = 'track'`, "", ""),
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
