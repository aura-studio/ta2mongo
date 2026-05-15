package talog

import "testing"

func TestIsUserType(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"user_set", true},
		{"user_setOnce", true},
		{"user_add", true},
		{"user_unset", true},
		{"user_del", true},
		{"user_append", true},
		{"user_uniq_append", true},
		{"track", false},
		{"track_update", false},
		{"track_overwrite", false},
		{"unknown", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			if got := IsUserType(tt.typ); got != tt.want {
				t.Errorf("IsUserType(%q) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestIsEventType(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"track", true},
		{"track_update", true},
		{"track_overwrite", true},
		{"user_set", false},
		{"user_del", false},
		{"unknown", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.typ, func(t *testing.T) {
			if got := IsEventType(tt.typ); got != tt.want {
				t.Errorf("IsEventType(%q) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}

func TestRecordCategory_User(t *testing.T) {
	r := &Record{Type: "user_set"}
	if r.Category() != CategoryUser {
		t.Errorf("expected CategoryUser, got %v", r.Category())
	}
}

func TestRecordCategory_Event(t *testing.T) {
	r := &Record{Type: "track"}
	if r.Category() != CategoryEvent {
		t.Errorf("expected CategoryEvent, got %v", r.Category())
	}
}

func TestRecordCategory_TrackUpdate(t *testing.T) {
	r := &Record{Type: "track_update"}
	if r.Category() != CategoryEvent {
		t.Errorf("expected CategoryEvent for track_update, got %v", r.Category())
	}
}

func TestRecordCategory_UserTypes(t *testing.T) {
	types := []string{"user_set", "user_setOnce", "user_add", "user_unset", "user_del", "user_append", "user_uniq_append"}
	for _, typ := range types {
		r := &Record{Type: typ}
		if r.Category() != CategoryUser {
			t.Errorf("expected CategoryUser for %s, got %v", typ, r.Category())
		}
	}
}

func TestRecordCategory_EventTypes(t *testing.T) {
	types := []string{"track", "track_update", "track_overwrite"}
	for _, typ := range types {
		r := &Record{Type: typ}
		if r.Category() != CategoryEvent {
			t.Errorf("expected CategoryEvent for %s, got %v", typ, r.Category())
		}
	}
}
