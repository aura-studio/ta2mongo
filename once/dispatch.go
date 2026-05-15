package once

import (
	"hash/fnv"

	"github.com/tidwall/gjson"
	"go.mongodb.org/mongo-driver/mongo"
)

// batch accumulates write models for a single collection.
type batch struct {
	models []mongo.WriteModel
	cap    int
}

func newBatch(capacity int) *batch {
	return &batch{
		models: make([]mongo.WriteModel, 0, capacity),
		cap:    capacity,
	}
}

func (b *batch) add(m mongo.WriteModel) { b.models = append(b.models, m) }
func (b *batch) full() bool             { return len(b.models) >= b.cap }
func (b *batch) empty() bool            { return len(b.models) == 0 }

func (b *batch) reset() {
	b.models = b.models[:0]
}

// extractRoutingKey performs a lightweight extraction of the user affinity key
// from a JSON log line. Priority: #account_id > #distinct_id.
func extractRoutingKey(line string) string {
	if v := gjson.Get(line, `#account_id`); v.Exists() && v.String() != "" {
		return v.String()
	}
	if v := gjson.Get(line, `#distinct_id`); v.Exists() && v.String() != "" {
		return v.String()
	}
	for _, envelope := range []string{"msg", "message", "log"} {
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

// routeIndex returns a consistent worker index for the given key using FNV-1a hash.
func routeIndex(key string, n int) int {
	if key == "" || n <= 0 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32()) % n
}
