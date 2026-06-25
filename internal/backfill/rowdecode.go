package backfill

import (
	"encoding/json"
	"fmt"
)

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
func EncodeRowAsJSONLine(headers []string, row []interface{}, defaultType string) (string, error) {
	if len(headers) != len(row) {
		return "", fmt.Errorf("backfill: row width %d does not match headers %d", len(row), len(headers))
	}

	obj := make(map[string]interface{}, len(headers)+1)
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

	buf, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("backfill: marshal row: %w", err)
	}
	return string(buf), nil
}
