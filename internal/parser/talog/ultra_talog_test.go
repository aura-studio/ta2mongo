package talog

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// PARSE-10: toString coercion for #account_id / #distinct_id
//
// toString returns "" for nil AND for any non-string value (numbers, bools,
// nested objects, etc.). The doc (PARSE-10) explicitly specifies this:
// "非字符串/nil 字段→\"\"" — so a NUMERIC #account_id coerces to "" (NOT its
// numeric string form, NOT "<nil>", and it must NOT panic). We assert the
// code's actual, documented behavior.
// ---------------------------------------------------------------------------

// TestUltraParse10_NumericAccountIDCoercesToEmpty verifies that a JSON-number
// #account_id does not panic and coerces to "" via toString. Because the
// record then has no string identity (account "" and no distinct_id), an
// event record fails the identity validation rule with a specific error.
func TestUltraParse10_NumericAccountIDCoercesToEmpty(t *testing.T) {
	// #account_id is a JSON number (12345), not a string.
	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"u1","#account_id":12345}`

	// Must not panic on a numeric id.
	rec, err := NewParser().ParseLine(line)

	// Numeric #account_id -> "" (toString drops non-strings), and with no
	// distinct_id the event record has no identity, so validation rejects it.
	if err == nil {
		t.Fatalf("expected identity validation error (numeric #account_id coerces to \"\"), got rec=%+v", rec)
	}
	if !strings.Contains(err.Error(), "at least one of #account_id or #distinct_id") {
		t.Fatalf("expected identity error from empty coerced id, got: %v", err)
	}
}

// TestUltraParse10_NumericAccountIDWithDistinctID isolates the coercion: a
// numeric #account_id alongside a valid string #distinct_id parses fine, and
// Record.AccountID is exactly "" (the numeric value is dropped, NOT "12345"
// and NOT "<nil>"), while DistinctID keeps its string value.
func TestUltraParse10_NumericAccountIDWithDistinctID(t *testing.T) {
	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"u1","#account_id":12345,"#distinct_id":"did-keep"}`

	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.AccountID != "" {
		t.Errorf("numeric #account_id should coerce to \"\" (not its numeric string), got %q", rec.AccountID)
	}
	if rec.DistinctID != "did-keep" {
		t.Errorf("expected DistinctID=did-keep, got %q", rec.DistinctID)
	}
	// The flattened Doc preserves the raw (non-string) value untouched; only the
	// typed Record.AccountID field is coerced. JSON numbers decode to float64.
	if v, ok := rec.Doc["#account_id"].(float64); !ok || v != 12345 {
		t.Errorf("expected Doc[#account_id] to keep raw numeric 12345 (float64), got %#v", rec.Doc["#account_id"])
	}
}

// TestUltraParse10_NilDistinctIDCoercesToEmpty verifies a JSON null value
// coerces to "" (not "<nil>") and does not panic. With a valid string
// #account_id the record still parses.
func TestUltraParse10_NilDistinctIDCoercesToEmpty(t *testing.T) {
	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"u1","#account_id":"acc-keep","#distinct_id":null}`

	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.DistinctID != "" {
		t.Errorf("null #distinct_id should coerce to \"\" (not \"<nil>\"), got %q", rec.DistinctID)
	}
	if rec.AccountID != "acc-keep" {
		t.Errorf("expected AccountID=acc-keep, got %q", rec.AccountID)
	}
}

// TestUltraParse10_NumericUUIDCoercesToEmpty verifies the same coercion on a
// required field: a numeric #uuid coerces to "" and triggers the "required"
// error (since uuid must be non-empty). Confirms toString is uniformly applied
// and a numeric required field neither panics nor leaks a stringified number.
func TestUltraParse10_NumericUUIDCoercesToEmpty(t *testing.T) {
	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":999,"#account_id":"a"}`

	_, err := NewParser().ParseLine(line)
	if err == nil {
		t.Fatalf("expected required error: numeric #uuid coerces to \"\"")
	}
	if !strings.Contains(err.Error(), "#type and #uuid are required") {
		t.Fatalf("expected '#type and #uuid are required' error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PARSE-11: EnvelopeKeys order and unwrapping
// ---------------------------------------------------------------------------

// TestUltraParse11_EnvelopeKeysOrder asserts the exact slice value and order:
// the envelope extraction tries msg, then message, then log.
func TestUltraParse11_EnvelopeKeysOrder(t *testing.T) {
	want := []string{"msg", "message", "log"}
	if len(EnvelopeKeys) != len(want) {
		t.Fatalf("EnvelopeKeys length = %d, want %d (%v)", len(EnvelopeKeys), len(want), EnvelopeKeys)
	}
	for i := range want {
		if EnvelopeKeys[i] != want[i] {
			t.Errorf("EnvelopeKeys[%d] = %q, want %q (full: %v)", i, EnvelopeKeys[i], want[i], EnvelopeKeys)
		}
	}
}

// TestUltraParse11_MsgUnwrapped verifies a TA payload wrapped under "msg" is
// unwrapped and parsed (the first envelope key).
func TestUltraParse11_MsgUnwrapped(t *testing.T) {
	inner := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"msg-uuid","#account_id":"acc-msg"}`
	line := `{"level":"info","msg":"` + strings.ReplaceAll(inner, `"`, `\"`) + `"}`

	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error unwrapping msg envelope: %v", err)
	}
	if rec.Type != "track" {
		t.Errorf("expected Type=track from unwrapped msg, got %q", rec.Type)
	}
	if rec.UUID != "msg-uuid" {
		t.Errorf("expected UUID=msg-uuid, got %q", rec.UUID)
	}
	if rec.AccountID != "acc-msg" {
		t.Errorf("expected AccountID=acc-msg, got %q", rec.AccountID)
	}
}

// TestUltraParse11_MsgWinsOverMessageAndLog verifies the order is honored: when
// the line carries a *valid* TA payload under "msg" and DIFFERENT payloads
// under "message" and "log", the "msg" key (first in EnvelopeKeys) wins.
func TestUltraParse11_MsgWinsOverMessageAndLog(t *testing.T) {
	msgInner := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"from-msg","#account_id":"a"}`
	messageInner := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"from-message","#account_id":"a"}`
	logInner := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"from-log","#account_id":"a"}`

	esc := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }
	line := `{"msg":"` + esc(msgInner) + `","message":"` + esc(messageInner) + `","log":"` + esc(logInner) + `"}`

	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.UUID != "from-msg" {
		t.Errorf("expected msg to win (UUID=from-msg), got %q", rec.UUID)
	}
}

// TestUltraParse11_FallsThroughToMessageWhenMsgInvalid verifies the loop skips
// an invalid "msg" (not a JSON object string) and continues to "message", the
// second key in order.
func TestUltraParse11_FallsThroughToMessageWhenMsgInvalid(t *testing.T) {
	messageInner := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"from-message","#account_id":"a"}`
	esc := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }

	// "msg" is a plain (non-JSON) string, so tryParseEnvelope skips it and
	// proceeds to "message".
	line := `{"msg":"plain non-json text","message":"` + esc(messageInner) + `"}`

	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.UUID != "from-message" {
		t.Errorf("expected fallthrough to message (UUID=from-message), got %q", rec.UUID)
	}
}

// TestUltraParse11_LogIsLastResort verifies the third key "log" is used when
// neither msg nor message carry a valid TA payload.
func TestUltraParse11_LogIsLastResort(t *testing.T) {
	logInner := `{"#type":"user_set","#time":"2024-01-01 00:00:00","#uuid":"from-log","#distinct_id":"d","properties":{"k":"v"}}`
	esc := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }

	line := `{"log":"` + esc(logInner) + `"}`

	rec, err := NewParser().ParseLine(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.UUID != "from-log" {
		t.Errorf("expected UUID=from-log, got %q", rec.UUID)
	}
	if rec.Doc["k"] != "v" {
		t.Errorf("expected flattened property k=v from log envelope, got %v", rec.Doc["k"])
	}
}
