package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/pflag"
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

// TestFlagOverridesEnvAndFile verifies the three input paths are consistent and
// correctly ordered: a --<key> flag (registered for every key) overrides both
// the TANGO_* env var and the config file value.
func TestFlagOverridesEnvAndFile(t *testing.T) {
	os.Setenv("TANGO_LOGGING_LEVEL", "warn")
	defer os.Unsetenv("TANGO_LOGGING_LEVEL")
	yaml := `
dao:
  mongo:
    uri: "mongodb://localhost/x"
logging:
  level: error
`
	flags := pflag.NewFlagSet("test", pflag.ContinueOnError)
	RegisterFlags(flags)
	// every key is a flag
	if flags.Lookup("dao.mongo.uri") == nil {
		t.Fatal("expected --dao.mongo.uri flag to be registered")
	}
	if flags.Lookup("process.pipeline.batchWorkers") == nil {
		t.Fatal("expected --process.pipeline.batchWorkers flag to be registered")
	}
	if err := flags.Parse([]string{"--logging.level", "debug"}); err != nil {
		t.Fatal(err)
	}

	c, err := Load(writeFile(t, "tango.yaml", yaml), flags)
	if err != nil {
		t.Fatal(err)
	}
	// flag (debug) beats env (warn) beats file (error)
	if c.Logging.Level != "debug" {
		t.Errorf("logging.level = %q, want debug (flag should win)", c.Logging.Level)
	}
}

func TestLoad_Gateway(t *testing.T) {
	yaml := `
dao:
  mongo:
    uri: "mongodb://localhost/gw"
process:
  batchSize: 250
role:
  gateway:
    addr: ":9090"
    defaultMode: pipeline
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
	if c.Role.Gateway.DefaultMode != "pipeline" {
		t.Errorf("role.gateway.defaultMode = %q", c.Role.Gateway.DefaultMode)
	}
	if c.Process.BatchSize != 250 {
		t.Errorf("process.batchSize = %d", c.Process.BatchSize)
	}
}
