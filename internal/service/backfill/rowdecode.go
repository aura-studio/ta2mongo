package backfill

import (
	"encoding/json"
	"fmt"
)

// EncodeRowAsJSONLine zips a result-page header row with a single data row and
// renders it as a JSON object identical in shape to the file-tail log lines
// the parser already expects. System fields ("#type", "#event_name", ...) are
// promoted to the top level. Non-system fields (user properties) are
// re-grouped under "properties" because that is the shape talog.Parser
// flattens via flattenPayload().
//
// The reason we re-serialise instead of constructing *talog.Record directly:
// the worker pipeline operates on a chan string, so any new data source has
// to produce strings. The CPU cost (single json.Marshal per row) is dwarfed
// by Mongo write latency and is preferable to teaching the worker about an
// alternate input type.
//
// Null Presto values arrive as Go nil and are simply omitted from the output
// so the parser does not see literal nulls in identity fields.
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
