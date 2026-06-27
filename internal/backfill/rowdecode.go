package backfill

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
)

// RowKeys reconciles a TA warehouse view row with the requirements of
// talog.buildRecord, which the live-ingest log format satisfies but the
// warehouse views do not. talog rejects any record whose #uuid is empty, and any
// record whose #time is empty — but the warehouse exposes the time as #event_time
// (event view) or #update_time (user view), not the literal #time, and the user
// view carries no per-row #uuid (it is keyed by #user_id). When EncodeRowAsJSONLine
// is given a non-nil *RowKeys it:
//
//   - fills #time from TimeColumn (e.g. "#event_time" for events, "#update_time"
//     for users) when #time is absent, falling back to Fallback (a single per-run
//     timestamp) when that column is missing/null too.
//   - when SynthUUID is set (user view only — the event view carries a real
//     #uuid), synthesizes a deterministic #uuid from the row's identity (#user_id,
//     else #account_id + #distinct_id) when #uuid is absent/empty. It is stable
//     across re-runs so the stored value never churns; real dedup still happens at
//     the write model, which keys the user document by the resolved #user_id — this
//     #uuid exists only to pass parsing.
type RowKeys struct {
	// TimeColumn is the source column copied into #time when the row carries no
	// #time of its own (#event_time for events, #update_time for users).
	TimeColumn string
	// Fallback is the #time used when neither #time nor TimeColumn is present on
	// the row. Callers set it once per run (a formatted timestamp).
	Fallback string
	// SynthUUID synthesizes a deterministic #uuid from identity when #uuid is
	// absent. Set for the user view (no per-row #uuid); left false for the event
	// view (which carries a real #uuid — a missing one there is a genuine defect
	// and should dead-letter, not be papered over).
	SynthUUID bool
}

// EncodeRowAsJSONLine zips a header row with one data row and renders a JSON
// object shaped identically to the file-tail TA log lines the parser already
// expects, so the output string can be fed straight through the normal upload
// pipeline (parse → filter → identity → write) — no custom write model needed.
//
//   - Null values (Go nil) are dropped entirely rather than serialized as
//     null, so the parser never sees literal nulls in identity fields.
//   - '$'-prefixed columns are dropped: they are TA query pseudo-columns
//     ($part_date / $part_event) — not part of the record, and unstorable
//     anyway (MongoDB/DocumentDB reject dollar-prefixed field names).
//   - System/identity fields — keys starting with '#' or '_' — are promoted to
//     the top level.
//   - All other columns are grouped under "properties" (the shape
//     talog.Parser flattens), omitted entirely when empty.
//   - When the row carries no "#type" (the user-state table has none; an event
//     row normally does), defaultType is injected so the parser routes the line
//     correctly: "track" for events, "user_setOnce"/"user_set" for user rows. A
//     defaultType of "" leaves #type absent.
//   - When keys is non-nil, the #time (and, for the user view, #uuid) that talog
//     requires but a warehouse row lacks are mapped/synthesized; see RowKeys.
//     A nil keys encodes the row verbatim.
func EncodeRowAsJSONLine(headers []string, row []interface{}, defaultType string, keys *RowKeys) (string, error) {
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
		if len(h) > 0 && h[0] == '$' {
			continue // TA query pseudo-column ($part_date/$part_event): not a record field, and unstorable
		}
		if len(h) > 0 && (h[0] == '#' || h[0] == '_') {
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
	if keys != nil {
		ensureRowKeys(obj, keys)
	}

	buf, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("backfill: marshal row: %w", err)
	}
	return string(buf), nil
}

// ensureRowKeys backfills the #time (always) and #uuid (user view only) that
// talog.buildRecord requires but a warehouse row does not carry in that shape.
func ensureRowKeys(obj map[string]interface{}, k *RowKeys) {
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
	if k.SynthUUID && isEmptyString(obj["#uuid"]) {
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
