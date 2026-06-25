package backfill

import (
	"encoding/json"
	"fmt"
)

// EncodeRowAsJSONLine zips a header row with one data row and renders a JSON
// object shaped identically to the file-tail TA log lines the parser already
// expects, so the output string can be fed straight into the engine's Upload
// face (which consumes a slice of log-line strings).
//
//   - Null values (Go nil) are dropped entirely rather than serialized as
//     null, so the parser never sees literal nulls in identity fields.
//   - System/identity fields — keys starting with '#', '_' or '$' — are
//     promoted to the top level.
//   - All other columns are grouped under "properties" (the shape
//     talog.Parser flattens), omitted entirely when empty.
func EncodeRowAsJSONLine(headers []string, row []interface{}) (string, error) {
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

	buf, err := json.Marshal(obj)
	if err != nil {
		return "", fmt.Errorf("backfill: marshal row: %w", err)
	}
	return string(buf), nil
}
