// Package pipeline provides shared pipeline components used by both
// the daemon (continuous tail) and once (one-shot) processing modes.
package pipeline

import (
	"hash/fnv"

	"github.com/tidwall/gjson"

	"rocket-nano/tools/tango/talog"
)

// ExtractRoutingKey performs a lightweight extraction of the user affinity key
// from a JSON log line. Priority: #account_id > #distinct_id.
// If neither is found (e.g. malformed JSON), returns "" which deterministically
// routes to worker 0.
func ExtractRoutingKey(line string) string {
	if v := gjson.Get(line, `#account_id`); v.Exists() && v.String() != "" {
		return v.String()
	}
	if v := gjson.Get(line, `#distinct_id`); v.Exists() && v.String() != "" {
		return v.String()
	}
	// Try envelope formats: msg, message, log fields may contain nested JSON.
	for _, envelope := range talog.EnvelopeKeys {
		inner := gjson.Get(line, envelope).String()
		if len(inner) < 2 || inner[0] != '{' {
			continue
		}
		if v := gjson.Get(inner, `#account_id`); v.Exists() && v.String() != "" {
			return v.String()
		}
		if v := gjson.Get(inner, `#distinct_id`); v.Exists() && v.String() != "" {
			return v.String()
		}
	}
	return ""
}

// RouteIndex returns a consistent worker index for the given key using FNV-1a hash.
func RouteIndex(key string, n int) int {
	if key == "" || n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32()) % n
}
