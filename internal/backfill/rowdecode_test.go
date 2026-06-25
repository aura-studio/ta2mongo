package backfill

import (
	"encoding/json"
	"testing"
)

func TestEncodeRowAsJSONLine_PromotesSystemFields(t *testing.T) {
	headers := []string{"#type", "#event_name", "#account_id", "#time", "level", "country"}
	row := []interface{}{"track", "login", "acc-1", "2026-05-01 10:00:00", float64(5), "CN"}

	line, err := EncodeRowAsJSONLine(headers, row, "")
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

	line, err := EncodeRowAsJSONLine(headers, row, "")
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
	_, err := EncodeRowAsJSONLine([]string{"a", "b"}, []interface{}{1}, "")
	if err == nil {
		t.Fatal("expected width mismatch error")
	}
}

func TestEncodeRowAsJSONLine_UnderscoreAndDollar(t *testing.T) {
	// Fields starting with _ or $ are treated as system-level (not properties).
	headers := []string{"_ts", "$part_date", "level"}
	row := []interface{}{int64(123), "2026-05-01", float64(7)}

	line, _ := EncodeRowAsJSONLine(headers, row, "")
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

	line, err := EncodeRowAsJSONLine(headers, row, "user_setOnce")
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
	line2, _ := EncodeRowAsJSONLine(h2, r2, "user_setOnce")
	var obj2 map[string]interface{}
	_ = json.Unmarshal([]byte(line2), &obj2)
	if obj2["#type"] != "track" {
		t.Errorf("#type = %v, want track (row value preserved)", obj2["#type"])
	}
}
