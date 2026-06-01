package remoteconfig

import (
	"testing"
	"time"

	"rocket-nano/tools/tango/config"
)

func baseConfig() config.Config {
	return config.Config{
		Mode:     config.ModeDaemon,
		Mongo:    config.MongoConfig{URI: "mongodb://local/db"},
		Pipeline: config.PipelineConfig{BatchSize: 1000, BatchWorkers: 2, FlushInterval: time.Second},
		Logging:  config.LoggingConfig{Level: "info"},
		Filter:   config.FilterConfig{Include: []string{`#type == "user_set"`}},
	}
}

func TestMerge_PerFieldOverride(t *testing.T) {
	base := baseConfig()
	doc := map[string]any{
		"filter":   map[string]any{"include": []any{`#type == "track"`, `#event_name == "PaymentOrderState"`}},
		"pipeline": map[string]any{"batchSize": 5000},
	}
	got, err := Merge(base, doc)
	if err != nil {
		t.Fatal(err)
	}
	// Overridden fields:
	if len(got.Filter.Include) != 2 {
		t.Errorf("Filter.Include = %v, want 2 entries", got.Filter.Include)
	}
	if got.Pipeline.BatchSize != 5000 {
		t.Errorf("BatchSize = %d, want 5000", got.Pipeline.BatchSize)
	}
	// Untouched fields keep local values:
	if got.Mongo.URI != "mongodb://local/db" {
		t.Errorf("Mongo.URI changed: %q", got.Mongo.URI)
	}
	if got.Pipeline.BatchWorkers != 2 {
		t.Errorf("BatchWorkers changed: %d", got.Pipeline.BatchWorkers)
	}
	if got.Logging.Level != "info" {
		t.Errorf("Logging.Level changed: %q", got.Logging.Level)
	}
	// base must not be mutated.
	if len(base.Filter.Include) != 1 {
		t.Errorf("base mutated: %v", base.Filter.Include)
	}
}

func TestMerge_DurationFromString(t *testing.T) {
	base := baseConfig()
	doc := map[string]any{"pipeline": map[string]any{"flushInterval": "2s"}}
	got, err := Merge(base, doc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pipeline.FlushInterval != 2*time.Second {
		t.Errorf("FlushInterval = %v, want 2s", got.Pipeline.FlushInterval)
	}
}

func TestMerge_NestedBackfill(t *testing.T) {
	base := baseConfig()
	doc := map[string]any{
		"backfillFilter": map[string]any{
			"table": "user",
		},
	}
	// backfillFilter is a nested struct; ensure nested merge works without
	// nuking sibling top-level filter.
	got, err := Merge(base, doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Filter.Include) != 1 || got.Filter.Include[0] != `#type == "user_set"` {
		t.Errorf("top-level Filter.Include clobbered: %v", got.Filter.Include)
	}
	if got.BackfillFilter.Table != "user" {
		t.Errorf("backfillFilter.table = %q, want user", got.BackfillFilter.Table)
	}
}

func TestMerge_EmptyDocReturnsBase(t *testing.T) {
	base := baseConfig()
	got, err := Merge(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pipeline.BatchSize != base.Pipeline.BatchSize {
		t.Errorf("empty doc changed config")
	}
}

func TestFilterChanged(t *testing.T) {
	a := baseConfig()
	b := baseConfig()
	if FilterChanged(a, b) {
		t.Errorf("identical configs reported as changed")
	}
	b.Filter.Include = []string{`#type == "track"`}
	if !FilterChanged(a, b) {
		t.Errorf("differing Filter.Include not detected")
	}
}
