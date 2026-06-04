package config

import (
	"testing"
	"time"
)

func TestLoadBytes_YAML(t *testing.T) {
	tree, err := LoadBytes([]byte(`
dao:
  mongo:
    uri: "mongodb://localhost/report"
    connectTimeout: "5s"
parser:
  filter:
    include: ['#type == "track"']
process:
  mode: pipeline
`))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	d := daoCfg(t, tree)
	if d.Mongo.URI != "mongodb://localhost/report" {
		t.Errorf("dao.mongo.uri = %q", d.Mongo.URI)
	}
	if d.Mongo.ConnectTimeout != 5*time.Second {
		t.Errorf("dao.mongo.connectTimeout = %v, want 5s", d.Mongo.ConnectTimeout)
	}
	if inc := parserCfg(t, tree).Filter.Include; len(inc) != 1 || inc[0] != `#type == "track"` {
		t.Errorf("parser.filter.include = %v", inc)
	}
	if got := procCfg(t, tree).Mode; got != "pipeline" {
		t.Errorf("process.mode = %q", got)
	}
}

func TestLoadBytes_JSON(t *testing.T) {
	// A gateway-compatible JSON config (the daemon/gateway examples' format).
	tree, err := LoadBytes([]byte(`{"role":{"mode":"gateway"},"dao":{"mongo":{"uri":"mongodb://localhost:27017/tango"}}}`))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if got := daoCfg(t, tree).Mongo.URI; got != "mongodb://localhost:27017/tango" {
		t.Errorf("dao.mongo.uri = %q", got)
	}
}

func TestLoadBytes_Empty_DefaultsOnly(t *testing.T) {
	tree, err := LoadBytes(nil)
	if err != nil {
		t.Fatalf("LoadBytes(nil): %v", err)
	}
	// Empty input still seeds defaults: mongo timeouts default even with no URI.
	if got := procCfg(t, tree).Mode; got != "batch" {
		t.Errorf("process.mode default = %q, want batch", got)
	}
}

func TestDetectConfigType(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:           "json",
		"  \n\t{\"a\":1}":   "json",
		"[1,2,3]":           "json",
		"dao:\n  mongo: {}": "yaml",
		"":                  "yaml",
	}
	for in, want := range cases {
		if got := detectConfigType([]byte(in)); got != want {
			t.Errorf("detectConfigType(%q) = %q, want %q", in, got, want)
		}
	}
}
