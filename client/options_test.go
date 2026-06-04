package client

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildOptions mirrors New's option application without connecting to MongoDB, so
// the config-loading options can be unit-tested in isolation.
func buildOptions(opts ...Option) *options {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	return o
}

const gatewayYAML = `
# gateway-compatible unified config; client consumes dao/parser/process only.
logging:
  level: debug
dao:
  mongo:
    uri: mongodb://localhost:27017/tango
parser:
  filter:
    include:
      - '#type == "track"'
process:
  mode: pipeline
  batchSize: 250
source:
  tailer:
    logPattern: ['/var/log/ta/*.log']
role:
  mode: gateway
`

func TestWithConfigBytes_GatewayCompatible(t *testing.T) {
	o := buildOptions(WithConfigBytes([]byte(gatewayYAML)))
	if o.err != nil {
		t.Fatalf("unexpected err: %v", o.err)
	}
	if o.dao.Mongo.URI != "mongodb://localhost:27017/tango" {
		t.Errorf("dao.mongo.uri = %q", o.dao.Mongo.URI)
	}
	// A leaf the config omitted keeps the engine default (re-ApplyDefaults).
	if o.dao.Mongo.ConnectTimeout != 10*time.Second {
		t.Errorf("connectTimeout = %v, want default 10s", o.dao.Mongo.ConnectTimeout)
	}
	if inc := o.parser.Filter.Include; len(inc) != 1 || inc[0] != `#type == "track"` {
		t.Errorf("parser.filter.include = %v", inc)
	}
	if o.proc.Mode != "pipeline" {
		t.Errorf("process.mode = %q", o.proc.Mode)
	}
	if o.proc.BatchSize != 250 {
		t.Errorf("process.batchSize = %d", o.proc.BatchSize)
	}
}

func TestWithConfigBytes_JSON(t *testing.T) {
	o := buildOptions(WithConfigBytes([]byte(`{"dao":{"mongo":{"uri":"mongodb://h/db"}}}`)))
	if o.err != nil {
		t.Fatalf("unexpected err: %v", o.err)
	}
	if o.dao.Mongo.URI != "mongodb://h/db" {
		t.Errorf("dao.mongo.uri = %q", o.dao.Mongo.URI)
	}
}

func TestWithConfigFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tango.yaml")
	if err := os.WriteFile(p, []byte(gatewayYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	o := buildOptions(WithConfigFile(p))
	if o.err != nil {
		t.Fatalf("unexpected err: %v", o.err)
	}
	if o.dao.Mongo.URI != "mongodb://localhost:27017/tango" {
		t.Errorf("dao.mongo.uri = %q", o.dao.Mongo.URI)
	}
}

// A later With* option overrides a value imported from the config file.
func TestConfigBytesThenOverride(t *testing.T) {
	o := buildOptions(
		WithConfigBytes([]byte(gatewayYAML)),
		WithDaoMongoURI("mongodb://override/db"),
		WithProcessMode("single"),
	)
	if o.err != nil {
		t.Fatalf("unexpected err: %v", o.err)
	}
	if o.dao.Mongo.URI != "mongodb://override/db" {
		t.Errorf("override failed: dao.mongo.uri = %q", o.dao.Mongo.URI)
	}
	if o.proc.Mode != "single" {
		t.Errorf("override failed: process.mode = %q", o.proc.Mode)
	}
}

func TestWithConfigBytes_Invalid(t *testing.T) {
	o := buildOptions(WithConfigBytes([]byte("dao: : : not valid yaml\n  - x")))
	if o.err == nil {
		t.Fatal("expected error for malformed config bytes")
	}
}

// New must surface a config-loading error instead of building a client.
func TestNew_PropagatesConfigError(t *testing.T) {
	_, err := New(WithConfigBytes([]byte("::::")))
	if err == nil {
		t.Fatal("expected New to fail on malformed config bytes")
	}
}
