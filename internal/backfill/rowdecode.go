package backfill

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
)

// UserKeys configures the synthesis that makes a v_user snapshot row satisfy the
// event-oriented requirements of talog.buildRecord. TA's user table (v_user_<id>)
// has neither a per-row #uuid nor a column literally named "#time" — it is keyed
// by #user_id and timestamps with a column like #update_time — yet the parser
// rejects any record whose #uuid is empty, and any user_* record whose #time is
// empty. When EncodeRowAsJSONLine is given a non-nil *UserKeys (the user table
// only; event rows pass nil and are left untouched), it:
//
//   - synthesizes a deterministic #uuid from the row's identity (#user_id, else
//     #account_id + #distinct_id) when #uuid is absent/empty. It is stable
//     across re-runs so the stored value never churns; real dedup still happens
//     at the write model, which keys the user document by the resolved #user_id —
//     this #uuid exists only to pass parsing.
//   - fills #time from TimeColumn (e.g. "#update_time") when #time is absent,
//     falling back to Fallback (a single per-run timestamp) when that column is
//     missing too.
type UserKeys struct {
	// TimeColumn is the v_user column whose value is copied into #time when the
	// row carries no #time of its own (e.g. "#update_time").
	TimeColumn string
	// Fallback is the #time used when neither #time nor TimeColumn is present on
	// the row. Callers set it once per run (a formatted timestamp).
	Fallback string
}

// EncodeRowAsJSONLine zips a header row with one data row and renders a JSON
// object shaped identically to the file-tail TA log lines the parser already
// expects, so the output string can be fed straight through the normal upload
// pipeline (parse → filter → identity → write) — no custom write model needed.
//
//   - Null values (Go nil) are dropped entirely rather than serialized as
//     null, so the parser never sees literal nulls in identity fields.
//   - System/identity fields — keys starting with '#', '_' or '$' — are
//     promoted to the top level.
//   - All other columns are grouped under "properties" (the shape
//     talog.Parser flattens), omitted entirely when empty.
//   - When the row carries no "#type" (the user-state table has none; an event
//     row normally does), defaultType is injected so the parser routes the line
//     correctly: "track" for events, "user_setOnce"/"user_set" for user rows. A
//     defaultType of "" leaves #type absent.
//   - When userKeys is non-nil (the user table only), the #uuid and #time that
//     talog requires but a v_user snapshot row lacks are synthesized; see
//     UserKeys. Event rows pass userKeys == nil and are encoded verbatim.
func EncodeRowAsJSONLine(headers []string, row []interface{}, defaultType string, userKeys *UserKeys) (string, error) {
	if len(headers) != len(row) {
		return "", fmt.Errorf("backfill: row width %d does not match headers %d", len(row), len(headers))
	}

	obj := make(map[string]interface{}, len(headers)+2)
	props := make(map[string]interface{})

	for i, h := range headers {
		v := row[i]
		if v == nil {
			continue
		}
		if len(h) > 0 && (h[0] == '#' || h[0] == '_' || h[0] == '$') {
			obj[h] = v
		} else {
			props[h] = v
		}
	}
	if len(props) > 0 {
		obj["properties"] = props
	}
	if defaultType != "" {
		if t, ok := obj["#type"].(string); !ok || t == "" {
			obj["#type"] = defaultType
		}
	}
	if userKeys != nil {
		ensureUserKeys(obj, userKeys)
	}

	buf, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("backfill: marshal row: %w", err)
	}
	return string(buf), nil
}

// ensureUserKeys backfills the #time and #uuid that talog.buildRecord requires
// but a v_user snapshot row does not carry. Called for user-table rows only;
// event rows are never passed here.
func ensureUserKeys(obj map[string]interface{}, k *UserKeys) {
	if isEmptyString(obj["#time"]) {
		if k.TimeColumn != "" {
			if tv := asString(obj[k.TimeColumn]); tv != "" {
				obj["#time"] = tv
			}
		}
		if isEmptyString(obj["#time"]) && k.Fallback != "" {
			obj["#time"] = k.Fallback
		}
	}
	if isEmptyString(obj["#uuid"]) {
		obj["#uuid"] = synthUserUUID(obj)
	}
}

// synthUserUUID derives a deterministic, canonical-UUID-shaped string from a
// user row's identity, so re-runs of the same row produce the same #uuid (no
// churn) while distinct users get distinct values. The write model keys the user
// document by the resolved #user_id, not this value — it exists only to satisfy
// talog's non-empty #uuid requirement. A row with no identity at all hashes a
// constant, but such a row fails talog's identity check anyway (and dead-letters).
func synthUserUUID(obj map[string]interface{}) string {
	identity := asString(obj["#user_id"])
	if identity == "" {
		identity = asString(obj["#account_id"]) + "\x1f" + asString(obj["#distinct_id"])
	}
	sum := sha1.Sum([]byte("tango/backfill/user\x00" + identity))
	var b [16]byte
	copy(b[:], sum[:16])
	b[6] = (b[6] & 0x0f) | 0x50 // version 5 (name-based SHA-1)
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// isEmptyString reports whether v is absent, not a string, or the empty string —
// matching talog.toString's "" treatment of a required field.
func isEmptyString(v interface{}) bool {
	s, ok := v.(string)
	return !ok || s == ""
}

// asString renders a cell value as a non-empty string when possible (strings
// verbatim, other scalars via fmt), or "" for nil. Used to coerce a time column
// (which TA may return as a string or a number) into the string #time wants.
func asString(v interface{}) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		return fmt.Sprintf("%v", t)
	}
}
