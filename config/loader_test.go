package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad_Unified(t *testing.T) {
	yaml := `
dao:
  mongo:
    uri: "mongodb://localhost/report"
parser:
  filter:
    include: ['#type == "track"']
source:
  tailer:
    logPattern: ["/tmp/.*\\.log"]
`
	c, err := Load(writeFile(t, "tango.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dao.Mongo.URI != "mongodb://localhost/report" {
		t.Errorf("dao.mongo.uri = %q", c.Dao.Mongo.URI)
	}
	if len(c.Parser.Filter.Include) != 1 || c.Parser.Filter.Include[0] != `#type == "track"` {
		t.Errorf("parser.filter.include = %v", c.Parser.Filter.Include)
	}
	if len(c.Source.Tailer.LogPattern) != 1 {
		t.Errorf("source.tailer.logPattern = %v", c.Source.Tailer.LogPattern)
	}
}

func TestLoad_TypedEnvOverrides(t *testing.T) {
	// Environment variables arrive as strings; Load must coerce them into int
	// fields (weak typing) as well as string / duration ones.
	os.Setenv("TANGO_PROCESS_PIPELINE_BATCHSIZE", "2500") // int
	defer os.Unsetenv("TANGO_PROCESS_PIPELINE_BATCHSIZE")
	yaml := `
dao:
  mongo:
    uri: "mongodb://localhost/report"
source:
  tailer:
    logPattern: ["/tmp/x.log"]
process:
  pipeline:
    batchSize: 1000
`
	c, err := Load(writeFile(t, "tango.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Process.Pipeline.BatchSize != 2500 {
		t.Errorf("process.pipeline.batchSize via int env = %d, want 2500", c.Process.Pipeline.BatchSize)
	}
}

func TestLoad_Gateway(t *testing.T) {
	yaml := `
dao:
  mongo:
    uri: "mongodb://localhost/gw"
role:
  gateway:
    addr: ":9090"
    upload:
      defaultMode: pipeline
      batchSize: 250
`
	c, err := Load(writeFile(t, "tango.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Dao.Mongo.URI != "mongodb://localhost/gw" {
		t.Errorf("dao.mongo.uri = %q", c.Dao.Mongo.URI)
	}
	if c.Role.Gateway.Addr != ":9090" {
		t.Errorf("role.gateway.addr = %q", c.Role.Gateway.Addr)
	}
	if c.Role.Gateway.Upload.DefaultMode != "pipeline" {
		t.Errorf("role.gateway.upload.defaultMode = %q", c.Role.Gateway.Upload.DefaultMode)
	}
	if c.Role.Gateway.Upload.BatchSize != 250 {
		t.Errorf("role.gateway.upload.batchSize = %d", c.Role.Gateway.Upload.BatchSize)
	}
}
