package backfill

import (
	"encoding/json"
	"testing"
)

func TestEncodeRowAsJSONLine_PromotesSystemFields(t *testing.T) {
	headers := []string{"#type", "#event_name", "#account_id", "#time", "level", "country"}
	row := []interface{}{"track", "login", "acc-1", "2026-05-01 10:00:00", float64(5), "CN"}

	line, err := EncodeRowAsJSONLine(headers, row, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatal(err)
	}

	checks := map[string]interface{}{
		"#type":       "track",
		"#event_name": "login",
		"#account_id": "acc-1",
		"#time":       "2026-05-01 10:00:00",
	}
	for k, want := range checks {
		if got := obj[k]; got != want {
			t.Errorf("%s = %v, want %v", k, got, want)
		}
	}

	props, ok := obj["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties missing or wrong type: %#v", obj["properties"])
	}
	if props["level"].(float64) != 5 {
		t.Errorf("properties.level = %v, want 5", props["level"])
	}
	if props["country"] != "CN" {
		t.Errorf("properties.country = %v, want CN", props["country"])
	}
}

func TestEncodeRowAsJSONLine_NilsOmitted(t *testing.T) {
	headers := []string{"#type", "level"}
	row := []interface{}{"track", nil}

	line, err := EncodeRowAsJSONLine(headers, row, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	var obj map[string]interface{}
	_ = json.Unmarshal([]byte(line), &obj)

	if _, ok := obj["properties"]; ok {
		t.Errorf("properties should be absent when only field was nil; got %v", obj["properties"])
	}
}

func TestEncodeRowAsJSONLine_WidthMismatch(t *testing.T) {
	_, err := EncodeRowAsJSONLine([]string{"a", "b"}, []interface{}{1}, "", nil)
	if err == nil {
		t.Fatal("expected width mismatch error")
	}
}

func TestEncodeRowAsJSONLine_UnderscoreAndDollar(t *testing.T) {
	// Fields starting with _ or $ are treated as system-level (not properties).
	headers := []string{"_ts", "$part_date", "level"}
	row := []interface{}{int64(123), "2026-05-01", float64(7)}

	line, _ := EncodeRowAsJSONLine(headers, row, "", nil)
	var obj map[string]interface{}
	_ = json.Unmarshal([]byte(line), &obj)

	if obj["_ts"] == nil || obj["$part_date"] == nil {
		t.Errorf("system fields not promoted: %#v", obj)
	}
	props, _ := obj["properties"].(map[string]interface{})
	if props == nil || props["level"] == nil {
		t.Errorf("level should be in properties: %#v", obj)
	}
}

func TestEncodeRowAsJSONLine_InjectsDefaultType(t *testing.T) {
	// A user-state row carries no #type; defaultType is injected so the parser
	// routes it (e.g. user_setOnce).
	headers := []string{"#user_id", "#account_id", "level"}
	row := []interface{}{int64(42), "acc-9", float64(3)}

	line, err := EncodeRowAsJSONLine(headers, row, "user_setOnce", nil)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	_ = json.Unmarshal([]byte(line), &obj)
	if obj["#type"] != "user_setOnce" {
		t.Errorf("#type = %v, want user_setOnce (injected)", obj["#type"])
	}

	// When the row already carries a #type, defaultType does not override it.
	h2 := []string{"#type", "level"}
	r2 := []interface{}{"track", float64(1)}
	line2, _ := EncodeRowAsJSONLine(h2, r2, "user_setOnce", nil)
	var obj2 map[string]interface{}
	_ = json.Unmarshal([]byte(line2), &obj2)
	if obj2["#type"] != "track" {
		t.Errorf("#type = %v, want track (row value preserved)", obj2["#type"])
	}
}

// TestEncodeRowAsJSONLine_UserKeysSynthesized covers the v_user fix: TA's user
// table carries neither #uuid nor a #time column, so talog would dead-letter the
// row; with UserKeys the encoder synthesizes both. This is exactly the shape
// TestBackfillUserPath feeds end-to-end.
func TestEncodeRowAsJSONLine_UserKeysSynthesized(t *testing.T) {
	headers := []string{"#account_id", "name", "age"}
	row := []interface{}{"acc-1", "Alice", float64(30)}
	keys := &UserKeys{TimeColumn: "#update_time", Fallback: "2026-06-26 00:00:00.000"}

	line, err := EncodeRowAsJSONLine(headers, row, "user_setOnce", keys)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		t.Fatal(err)
	}

	// #uuid must be synthesized (non-empty) so talog.buildRecord accepts the row.
	uuid, _ := obj["#uuid"].(string)
	if uuid == "" {
		t.Fatalf("#uuid not synthesized: %#v", obj)
	}
	// No #time and no #update_time column → fallback timestamp is used.
	if obj["#time"] != "2026-06-26 00:00:00.000" {
		t.Errorf("#time = %v, want fallback timestamp", obj["#time"])
	}
	if obj["#type"] != "user_setOnce" {
		t.Errorf("#type = %v, want user_setOnce", obj["#type"])
	}
	if obj["#account_id"] != "acc-1" {
		t.Errorf("#account_id = %v, want acc-1", obj["#account_id"])
	}
}

// TestEncodeRowAsJSONLine_UserTimeColumnMapped checks the time column is mapped
// into #time when present, in preference to the fallback.
func TestEncodeRowAsJSONLine_UserTimeColumnMapped(t *testing.T) {
	headers := []string{"#user_id", "#account_id", "#update_time", "name"}
	row := []interface{}{int64(7), "acc-7", "2026-05-01 09:00:00.000", "Carol"}
	keys := &UserKeys{TimeColumn: "#update_time", Fallback: "2099-01-01 00:00:00.000"}

	line, _ := EncodeRowAsJSONLine(headers, row, "user_setOnce", keys)
	var obj map[string]interface{}
	_ = json.Unmarshal([]byte(line), &obj)

	if obj["#time"] != "2026-05-01 09:00:00.000" {
		t.Errorf("#time = %v, want value mapped from #update_time (not fallback)", obj["#time"])
	}
}

// TestSynthUserUUID_DeterministicAndDistinct guards the re-run-stable, per-user
// property of the synthesized #uuid: same identity → same uuid (no churn),
// different identity → different uuid.
func TestSynthUserUUID_DeterministicAndDistinct(t *testing.T) {
	a := map[string]interface{}{"#user_id": "100"}
	b := map[string]interface{}{"#user_id": "100"}
	c := map[string]interface{}{"#user_id": "200"}

	if synthUserUUID(a) != synthUserUUID(b) {
		t.Error("same #user_id must yield the same #uuid (re-run stability)")
	}
	if synthUserUUID(a) == synthUserUUID(c) {
		t.Error("distinct #user_id must yield distinct #uuid")
	}

	// Falls back to #account_id/#distinct_id when #user_id is absent, and that
	// path is independent of the #user_id path.
	d := map[string]interface{}{"#account_id": "acc-1"}
	e := map[string]interface{}{"#account_id": "acc-1", "#distinct_id": "d-9"}
	if synthUserUUID(d) == synthUserUUID(e) {
		t.Error("different identity composition must yield distinct #uuid")
	}
	if synthUserUUID(d) == "" {
		t.Error("#uuid must be non-empty even with only #account_id")
	}
}

// TestEncodeRowAsJSONLine_EventPathUnaffected confirms event rows (userKeys nil)
// are NOT given a synthesized #uuid/#time — an event lacking #uuid stays without
// one and dead-letters at the parser, exactly as before the fix.
func TestEncodeRowAsJSONLine_EventPathUnaffected(t *testing.T) {
	headers := []string{"#event_name", "#account_id", "level"}
	row := []interface{}{"login", "acc-1", float64(2)}

	line, _ := EncodeRowAsJSONLine(headers, row, "track", nil)
	var obj map[string]interface{}
	_ = json.Unmarshal([]byte(line), &obj)

	if _, ok := obj["#uuid"]; ok {
		t.Errorf("event path must not synthesize #uuid; got %v", obj["#uuid"])
	}
	if _, ok := obj["#time"]; ok {
		t.Errorf("event path must not synthesize #time; got %v", obj["#time"])
	}
}
