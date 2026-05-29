package config

import (
	"path/filepath"
	"testing"
)

// TestExampleConfigsLoad ensures every shipped example config and the
// top-level tango.yaml parse cleanly under the current (nested) schema, so the
// samples can't silently drift from the code.
func TestExampleConfigsLoad(t *testing.T) {
	paths, err := filepath.Glob("../examples/config/*/tango.yaml")
	if err != nil {
		t.Fatal(err)
	}
	paths = append(paths, "../tango.yaml")
	if len(paths) < 2 {
		t.Fatalf("expected example configs + tango.yaml, found %v", paths)
	}

	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			cfg, err := Load(p, nil)
			if err != nil {
				t.Fatalf("Load(%s): %v", p, err)
			}
			// Defaults must have populated the nested sections.
			if cfg.Mongo.ConnectTimeout <= 0 || cfg.Pipeline.BatchSize <= 0 {
				t.Errorf("%s: defaults not applied: %+v", p, cfg)
			}
		})
	}
}
