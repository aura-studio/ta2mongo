package remoteconfig

import (
	"testing"
	"time"

	"rocket-nano/tools/tango/config"
)

func baseConfig() config.Config {
	return config.Config{
		Mode:          config.ModeDaemon,
		MongoURI:      "mongodb://local/db",
		BatchSize:     1000,
		BatchWorkers:  2,
		FlushInterval: time.Second,
		LogLevel:      "info",
		FilterInclude: []string{`#type == "user_set"`},
		FilterExclude: nil,
	}
}

func TestMerge_PerFieldOverride(t *testing.T) {
	base := baseConfig()
	doc := map[string]any{
		"filterInclude": []any{`#type == "track"`, `#event_name == "PaymentOrderState"`},
		"batchSize":     5000,
	}
	got, err := Merge(base, doc)
	if err != nil {
		t.Fatal(err)
	}
	// Overridden fields:
	if len(got.FilterInclude) != 2 {
		t.Errorf("FilterInclude = %v, want 2 entries", got.FilterInclude)
	}
	if got.BatchSize != 5000 {
		t.Errorf("BatchSize = %d, want 5000", got.BatchSize)
	}
	// Untouched fields keep local values:
	if got.MongoURI != "mongodb://local/db" {
		t.Errorf("MongoURI changed: %q", got.MongoURI)
	}
	if got.BatchWorkers != 2 {
		t.Errorf("BatchWorkers changed: %d", got.BatchWorkers)
	}
	if got.LogLevel != "info" {
		t.Errorf("LogLevel changed: %q", got.LogLevel)
	}
	// base must not be mutated.
	if len(base.FilterInclude) != 1 {
		t.Errorf("base mutated: %v", base.FilterInclude)
	}
}

func TestMerge_DurationFromString(t *testing.T) {
	base := baseConfig()
	doc := map[string]any{"flushInterval": "2s"}
	got, err := Merge(base, doc)
	if err != nil {
		t.Fatal(err)
	}
	if got.FlushInterval != 2*time.Second {
		t.Errorf("FlushInterval = %v, want 2s", got.FlushInterval)
	}
}

func TestMerge_NestedBackfill(t *testing.T) {
	base := baseConfig()
	doc := map[string]any{
		"backfill": map[string]any{
			"filterInclude": []any{`country == "CN"`},
		},
	}
	// backfill is a nested struct; ensure nested merge works without nuking
	// sibling top-level filter.
	got, err := Merge(base, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.FilterInclude) != 1 || got.FilterInclude[0] != `#type == "user_set"` {
		t.Errorf("top-level FilterInclude clobbered: %v", got.FilterInclude)
	}
}

func TestMerge_EmptyDocReturnsBase(t *testing.T) {
	base := baseConfig()
	got, err := Merge(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.BatchSize != base.BatchSize {
		t.Errorf("empty doc changed config")
	}
}

func TestFilterChanged(t *testing.T) {
	a := baseConfig()
	b := baseConfig()
	if FilterChanged(a, b) {
		t.Errorf("identical configs reported as changed")
	}
	b.FilterInclude = []string{`#type == "track"`}
	if !FilterChanged(a, b) {
		t.Errorf("differing FilterInclude not detected")
	}
}
