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

func TestLoadStandalone_Unified(t *testing.T) {
	yaml := `
runtime:
  mongo:
    uri: "mongodb://localhost/report"
report:
  source:
    logPattern: ["/tmp/.*\\.log"]
  filter:
    include: ['#type == "track"']
`
	rc, rt, err := LoadStandalone(writeFile(t, "standalone.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Mongo.URI != "mongodb://localhost/report" {
		t.Errorf("LoadStandalone Mongo.URI = %q", rt.Mongo.URI)
	}
	if len(rt.Filter.Include) != 1 || rt.Filter.Include[0] != `#type == "track"` {
		t.Errorf("report.filter -> runtime Filter = %v", rt.Filter.Include)
	}
	if len(rc.Report.Source.LogPattern) != 1 {
		t.Errorf("RoleConfig logPattern = %v", rc.Report.Source.LogPattern)
	}
}

func TestLoadStandalone_TypedEnvOverrides(t *testing.T) {
	// Environment variables arrive as strings; the role loader must coerce them
	// into int fields (weak typing) as well as string / duration ones.
	os.Setenv("TANGO_REPORT_PIPELINE_BATCHSIZE", "2500") // int
	defer os.Unsetenv("TANGO_REPORT_PIPELINE_BATCHSIZE")
	yaml := `
runtime:
  mongo:
    uri: "mongodb://localhost/report"
report:
  source:
    logPattern: ["/tmp/x.log"]
  pipeline:
    batchSize: 1000
`
	_, rt, err := LoadStandalone(writeFile(t, "standalone.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Pipeline.BatchSize != 2500 {
		t.Errorf("pipeline.batchSize via int env = %d, want 2500", rt.Pipeline.BatchSize)
	}
}

func TestLoadStandalone_RequiresLogPattern(t *testing.T) {
	yaml := "runtime:\n  mongo:\n    uri: \"mongodb://localhost/report\"\n"
	if _, _, err := LoadStandalone(writeFile(t, "standalone.yaml", yaml), nil); err == nil {
		t.Fatal("expected error: standalone without logPattern")
	}
}

func TestLoadGateway_Unified(t *testing.T) {
	yaml := `
runtime:
  mongo:
    uri: "mongodb://localhost/gw"
gateway:
  addr: ":9090"
upload:
  string:
    batchSize: 250
  file:
    checkpointCollection: custom_ckpt
`
	_, cc, err := LoadGateway(writeFile(t, "gateway.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Mongo.URI != "mongodb://localhost/gw" {
		t.Errorf("gateway Mongo.URI = %q", cc.Mongo.URI)
	}
	if cc.Server.Addr != ":9090" {
		t.Errorf("gateway addr = %q", cc.Server.Addr)
	}
	if cc.StringUpload.BatchSize != 250 {
		t.Errorf("upload.string.batchSize = %d", cc.StringUpload.BatchSize)
	}
	if cc.FileUpload.CheckpointCollection != "custom_ckpt" {
		t.Errorf("upload.file.checkpointCollection = %q", cc.FileUpload.CheckpointCollection)
	}
}

// TestExampleRoleConfigsLoad ensures every shipped role example (max + min, in
// both YAML and JSON) parses and validates under its loader.
func TestExampleRoleConfigsLoad(t *testing.T) {
	standalone := []string{
		"../examples/config/standalone/standalone.max.yaml",
		"../examples/config/standalone/standalone.min.yaml",
		"../examples/config/standalone/standalone.max.json",
		"../examples/config/standalone/standalone.min.json",
	}
	for _, p := range standalone {
		t.Run(p, func(t *testing.T) {
			if _, _, err := LoadStandalone(p, nil); err != nil {
				t.Fatalf("LoadStandalone(%s): %v", p, err)
			}
		})
	}
	gateway := []string{
		"../examples/config/gateway/gateway.max.yaml",
		"../examples/config/gateway/gateway.min.yaml",
		"../examples/config/gateway/gateway.max.json",
		"../examples/config/gateway/gateway.min.json",
	}
	for _, p := range gateway {
		t.Run(p, func(t *testing.T) {
			if _, _, err := LoadGateway(p, nil); err != nil {
				t.Fatalf("LoadGateway(%s): %v", p, err)
			}
		})
	}
}
