package config

import (
	"path/filepath"
	"testing"
)

// TestExampleConfigsLoad ensures every shipped example config (yaml + json,
// daemon + gateway, max + min) loads cleanly through Load, so the examples never
// drift from the schema.
func TestExampleConfigsLoad(t *testing.T) {
	matches, err := filepath.Glob(filepath.Join("..", "examples", "config", "*", "*.*"))
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, m := range matches {
		switch filepath.Ext(m) {
		case ".yaml", ".yml", ".json":
			files = append(files, m)
		}
	}
	if len(files) == 0 {
		t.Fatal("no example config files found")
	}
	for _, f := range files {
		f := f
		t.Run(filepath.Base(filepath.Dir(f))+"/"+filepath.Base(f), func(t *testing.T) {
			if _, err := Load(f, nil); err != nil {
				t.Fatalf("Load(%s): %v", f, err)
			}
		})
	}
}

// TestMaxConfigsAreComplete checks each *.max.* example populates all six
// schema sections (with representative leaf fields), so "max" stays a full
// reference of the unified schema.
func TestMaxConfigsAreComplete(t *testing.T) {
	maxes, _ := filepath.Glob(filepath.Join("..", "examples", "config", "*", "*.max.*"))
	if len(maxes) == 0 {
		t.Fatal("no *.max.* example files found")
	}
	for _, f := range maxes {
		ext := filepath.Ext(f)
		if ext != ".yaml" && ext != ".yml" && ext != ".json" {
			continue
		}
		f := f
		t.Run(filepath.Base(filepath.Dir(f))+"/"+filepath.Base(f), func(t *testing.T) {
			c, err := Load(f, nil)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Logging.Level == "" {
				t.Error("logging.level empty")
			}
			if c.Dao.Mongo.URI == "" {
				t.Error("dao.mongo.uri empty")
			}
			if len(c.Parser.Filter.Include) == 0 {
				t.Error("parser.filter.include empty")
			}
			if len(c.Source.Tailer.LogPattern) == 0 {
				t.Error("source.tailer.logPattern empty")
			}
			if c.Process.Pipeline.BatchWorkers == 0 {
				t.Error("process.pipeline.batchWorkers unset")
			}
			if c.Role.Daemon == nil {
				t.Error("role.daemon missing")
			}
			if c.Role.Gateway.Addr == "" {
				t.Error("role.gateway.addr empty")
			}
		})
	}
}
