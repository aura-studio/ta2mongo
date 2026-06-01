package talog

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Direct TA payload parsing
// ---------------------------------------------------------------------------

func TestParseLine_Track(t *testing.T) {
	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"u1","#account_id":"acc1","#distinct_id":"did1","properties":{"ip":"1.2.3.4"}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "track" {
		t.Errorf("expected Type=track, got %s", rec.Type)
	}
	if rec.UUID != "u1" {
		t.Errorf("expected UUID=u1, got %s", rec.UUID)
	}
	if rec.AccountID != "acc1" {
		t.Errorf("expected AccountID=acc1, got %s", rec.AccountID)
	}
	if rec.DistinctID != "did1" {
		t.Errorf("expected DistinctID=did1, got %s", rec.DistinctID)
	}
	// properties should be flattened
	if rec.Doc["ip"] != "1.2.3.4" {
		t.Errorf("expected ip=1.2.3.4, got %v", rec.Doc["ip"])
	}
	if _, ok := rec.Doc["properties"]; ok {
		t.Error("expected properties to be deleted after flattening")
	}
}

func TestParseLine_UserSet(t *testing.T) {
	line := `{"#type":"user_set","#time":"2024-01-01 00:00:00","#uuid":"u2","#account_id":"acc2","properties":{"name":"Alice","age":30}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "user_set" {
		t.Errorf("expected Type=user_set, got %s", rec.Type)
	}
	if rec.Doc["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", rec.Doc["name"])
	}
}

func TestParseLine_UserSetOnce(t *testing.T) {
	line := `{"#type":"user_setOnce","#time":"2024-01-01 00:00:00","#uuid":"u3","#distinct_id":"did3","properties":{"first_login":"2024-01-01"}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "user_setOnce" {
		t.Errorf("expected Type=user_setOnce, got %s", rec.Type)
	}
}

func TestParseLine_UserAdd(t *testing.T) {
	line := `{"#type":"user_add","#time":"2024-01-01 00:00:00","#uuid":"u4","#account_id":"acc4","properties":{"coins":100}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "user_add" {
		t.Errorf("expected Type=user_add, got %s", rec.Type)
	}
}

func TestParseLine_UserUnset(t *testing.T) {
	line := `{"#type":"user_unset","#time":"2024-01-01 00:00:00","#uuid":"u5","#account_id":"acc5","properties":{"old_field":true}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "user_unset" {
		t.Errorf("expected Type=user_unset, got %s", rec.Type)
	}
}

func TestParseLine_UserDel(t *testing.T) {
	line := `{"#type":"user_del","#time":"2024-01-01 00:00:00","#uuid":"u6","#account_id":"acc6"}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "user_del" {
		t.Errorf("expected Type=user_del, got %s", rec.Type)
	}
}

func TestParseLine_UserAppend(t *testing.T) {
	line := `{"#type":"user_append","#time":"2024-01-01 00:00:00","#uuid":"u7","#account_id":"acc7","properties":{"tags":["vip"]}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "user_append" {
		t.Errorf("expected Type=user_append, got %s", rec.Type)
	}
}

func TestParseLine_UserUniqAppend(t *testing.T) {
	line := `{"#type":"user_uniq_append","#time":"2024-01-01 00:00:00","#uuid":"u8","#account_id":"acc8","properties":{"tags":["vip"]}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "user_uniq_append" {
		t.Errorf("expected Type=user_uniq_append, got %s", rec.Type)
	}
}

func TestParseLine_TrackUpdate(t *testing.T) {
	line := `{"#type":"track_update","#event_name":"purchase","#time":"2024-01-01 00:00:00","#uuid":"u9","#account_id":"acc9","properties":{"amount":99.9}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "track_update" {
		t.Errorf("expected Type=track_update, got %s", rec.Type)
	}
}

func TestParseLine_TrackOverwrite(t *testing.T) {
	line := `{"#type":"track_overwrite","#event_name":"purchase","#time":"2024-01-01 00:00:00","#uuid":"u10","#account_id":"acc10","properties":{"amount":99.9}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "track_overwrite" {
		t.Errorf("expected Type=track_overwrite, got %s", rec.Type)
	}
}

// ---------------------------------------------------------------------------
// Envelope format parsing
// ---------------------------------------------------------------------------

func TestParseLine_EnvelopeMsg(t *testing.T) {
	inner := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"e1","#account_id":"acc1"}`
	line := `{"level":"info","msg":` + `"` + strings.ReplaceAll(inner, `"`, `\"`) + `"` + `}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "track" {
		t.Errorf("expected Type=track, got %s", rec.Type)
	}
	if rec.UUID != "e1" {
		t.Errorf("expected UUID=e1, got %s", rec.UUID)
	}
}

func TestParseLine_EnvelopeMessage(t *testing.T) {
	inner := `{"#type":"user_set","#time":"2024-01-01 00:00:00","#uuid":"e2","#distinct_id":"did2","properties":{"name":"Bob"}}`
	line := `{"level":"info","message":` + `"` + strings.ReplaceAll(inner, `"`, `\"`) + `"` + `}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "user_set" {
		t.Errorf("expected Type=user_set, got %s", rec.Type)
	}
	if rec.Doc["name"] != "Bob" {
		t.Errorf("expected name=Bob, got %v", rec.Doc["name"])
	}
}

func TestParseLine_EnvelopeLog(t *testing.T) {
	inner := `{"#type":"track","#event_name":"click","#time":"2024-01-01 00:00:00","#uuid":"e3","#account_id":"acc3"}`
	line := `{"ts":"2024-01-01","log":` + `"` + strings.ReplaceAll(inner, `"`, `\"`) + `"` + `}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Type != "track" {
		t.Errorf("expected Type=track, got %s", rec.Type)
	}
}

// ---------------------------------------------------------------------------
// Validation error cases
// ---------------------------------------------------------------------------

func TestParseLine_InvalidJSON(t *testing.T) {
	_, err := NewParser().ParseLine("not json at all")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("expected 'invalid JSON' in error, got: %v", err)
	}
}

func TestParseLine_NotTAPayload(t *testing.T) {
	_, err := NewParser().ParseLine(`{"foo":"bar"}`)
	if err == nil {
		t.Fatal("expected error for non-TA payload")
	}
	if !strings.Contains(err.Error(), "not a ThinkingData payload") {
		t.Errorf("expected 'not a ThinkingData payload' in error, got: %v", err)
	}
}

func TestParseLine_MissingType(t *testing.T) {
	_, err := NewParser().ParseLine(`{"#time":"2024-01-01","#uuid":"u1","#account_id":"a"}`)
	if err == nil {
		t.Fatal("expected error for missing #type")
	}
}

func TestParseLine_MissingUUID(t *testing.T) {
	_, err := NewParser().ParseLine(`{"#type":"track","#event_name":"login","#time":"2024-01-01","#account_id":"a"}`)
	if err == nil {
		t.Fatal("expected error for missing #uuid")
	}
}

func TestParseLine_MissingTime_UserType(t *testing.T) {
	_, err := NewParser().ParseLine(`{"#type":"user_set","#uuid":"u1","#account_id":"a"}`)
	if err == nil {
		t.Fatal("expected error for missing #time on user record")
	}
	if !strings.Contains(err.Error(), "#time is required") {
		t.Errorf("expected '#time is required' in error, got: %v", err)
	}
}

func TestParseLine_MissingTime_EventType(t *testing.T) {
	_, err := NewParser().ParseLine(`{"#type":"track","#event_name":"login","#uuid":"u1","#account_id":"a"}`)
	if err == nil {
		t.Fatal("expected error for missing #time on event record")
	}
}

func TestParseLine_MissingEventName(t *testing.T) {
	_, err := NewParser().ParseLine(`{"#type":"track","#time":"2024-01-01","#uuid":"u1","#account_id":"a"}`)
	if err == nil {
		t.Fatal("expected error for missing #event_name on track record")
	}
	if !strings.Contains(err.Error(), "#event_name is required") {
		t.Errorf("expected '#event_name is required' in error, got: %v", err)
	}
}

func TestParseLine_MissingIdentity_User(t *testing.T) {
	_, err := NewParser().ParseLine(`{"#type":"user_set","#time":"2024-01-01","#uuid":"u1"}`)
	if err == nil {
		t.Fatal("expected error for missing identity on user record")
	}
	if !strings.Contains(err.Error(), "at least one of #account_id or #distinct_id") {
		t.Errorf("expected identity error, got: %v", err)
	}
}

func TestParseLine_MissingIdentity_Event(t *testing.T) {
	_, err := NewParser().ParseLine(`{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1"}`)
	if err == nil {
		t.Fatal("expected error for missing identity on event record")
	}
}

func TestParseLine_UnsupportedType(t *testing.T) {
	_, err := NewParser().ParseLine(`{"#type":"unknown_type","#time":"2024-01-01","#uuid":"u1","#account_id":"a"}`)
	if err == nil {
		t.Fatal("expected error for unsupported #type")
	}
	if !strings.Contains(err.Error(), "unsupported #type") {
		t.Errorf("expected 'unsupported #type' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestParseLine_OnlyAccountID(t *testing.T) {
	line := `{"#type":"user_set","#time":"2024-01-01","#uuid":"u1","#account_id":"acc1","properties":{"x":1}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.AccountID != "acc1" {
		t.Errorf("expected AccountID=acc1, got %s", rec.AccountID)
	}
	if rec.DistinctID != "" {
		t.Errorf("expected empty DistinctID, got %s", rec.DistinctID)
	}
}

func TestParseLine_OnlyDistinctID(t *testing.T) {
	line := `{"#type":"user_set","#time":"2024-01-01","#uuid":"u1","#distinct_id":"did1","properties":{"x":1}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.DistinctID != "did1" {
		t.Errorf("expected DistinctID=did1, got %s", rec.DistinctID)
	}
	if rec.AccountID != "" {
		t.Errorf("expected empty AccountID, got %s", rec.AccountID)
	}
}

func TestParseLine_BothIDs(t *testing.T) {
	line := `{"#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"u1","#account_id":"acc1","#distinct_id":"did1"}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.AccountID != "acc1" || rec.DistinctID != "did1" {
		t.Errorf("expected both IDs, got account=%s distinct=%s", rec.AccountID, rec.DistinctID)
	}
}

func TestParseLine_FlattenedProperties(t *testing.T) {
	line := `{"#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"u1","#account_id":"a","properties":{"nested_key":"val","count":42}}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Doc["nested_key"] != "val" {
		t.Errorf("expected nested_key=val, got %v", rec.Doc["nested_key"])
	}
	if _, ok := rec.Doc["properties"]; ok {
		t.Error("properties key should be removed after flattening")
	}
}

func TestParseLine_DocContainsMetaFields(t *testing.T) {
	line := `{"#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"u1","#account_id":"a"}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Doc["#time"] != "2024-01-01" {
		t.Errorf("expected #time in doc, got %v", rec.Doc["#time"])
	}
	if rec.Doc["#uuid"] != "u1" {
		t.Errorf("expected #uuid in doc, got %v", rec.Doc["#uuid"])
	}
	if rec.Doc["_ts"] == nil {
		t.Error("expected _ts to be set in doc")
	}
}

func TestParseLine_EmptyLine(t *testing.T) {
	_, err := NewParser().ParseLine("")
	if err == nil {
		t.Fatal("expected error for empty line")
	}
}

func TestParseLine_EmptyJSON(t *testing.T) {
	_, err := NewParser().ParseLine("{}")
	if err == nil {
		t.Fatal("expected error for empty JSON object")
	}
}

func TestParseLine_EnvelopeWithNonJSONMsg(t *testing.T) {
	// msg field is not a JSON string - should not crash
	_, err := NewParser().ParseLine(`{"msg":"this is not json"}`)
	if err == nil {
		t.Fatal("expected error for non-JSON msg field")
	}
}

func TestParseLine_EnvelopeWithEmptyMsg(t *testing.T) {
	_, err := NewParser().ParseLine(`{"msg":""}`)
	if err == nil {
		t.Fatal("expected error for empty msg field")
	}
}

func TestParseLine_NoPropertiesField(t *testing.T) {
	line := `{"#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"u1","#account_id":"a","custom_field":"direct"}`
	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Doc["custom_field"] != "direct" {
		t.Errorf("expected custom_field=direct, got %v", rec.Doc["custom_field"])
	}
}
