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

func TestLoadReport_Unified(t *testing.T) {
	yaml := `
runtime:
  mongo:
    uri: "mongodb://localhost/report"
report:
  source:
    logPattern: ["/tmp/.*\\.log"]
  filter:
    include: ['#type == "track"']
remoteConfig:
  enabled: true
`
	rc, rt, err := LoadReport(writeFile(t, "report.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Mongo.URI != "mongodb://localhost/report" {
		t.Errorf("LoadReport Mongo.URI = %q", rt.Mongo.URI)
	}
	if len(rt.Filter.Include) != 1 || rt.Filter.Include[0] != `#type == "track"` {
		t.Errorf("report.filter -> runtime Filter = %v", rt.Filter.Include)
	}
	// The report loader never enables the task worker, regardless of file.
	if rt.Worker.Enabled {
		t.Error("LoadReport unexpectedly enabled task worker")
	}
	if !rt.RemoteConfig.Enabled {
		t.Error("LoadReport did not honour remoteConfig.enabled")
	}
	if len(rc.Report.Source.LogPattern) != 1 {
		t.Errorf("RoleConfig logPattern = %v", rc.Report.Source.LogPattern)
	}
}

func TestLoadReport_TypedEnvOverrides(t *testing.T) {
	// Environment variables arrive as strings; the role loader must coerce them
	// into int / bool fields (weak typing) as well as string / duration ones.
	os.Setenv("TANGO_REPORT_PIPELINE_BATCHSIZE", "2500") // int
	os.Setenv("TANGO_REMOTECONFIG_ENABLED", "true")      // bool
	defer func() {
		os.Unsetenv("TANGO_REPORT_PIPELINE_BATCHSIZE")
		os.Unsetenv("TANGO_REMOTECONFIG_ENABLED")
	}()
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
	_, rt, err := LoadReport(writeFile(t, "report.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Pipeline.BatchSize != 2500 {
		t.Errorf("pipeline.batchSize via int env = %d, want 2500", rt.Pipeline.BatchSize)
	}
	if !rt.RemoteConfig.Enabled {
		t.Error("remoteConfig.enabled via bool env did not take effect")
	}
}

func TestLoadReport_RequiresLogPattern(t *testing.T) {
	yaml := "runtime:\n  mongo:\n    uri: \"mongodb://localhost/report\"\n"
	if _, _, err := LoadReport(writeFile(t, "report.yaml", yaml), nil); err == nil {
		t.Fatal("expected error: report without logPattern")
	}
}

func TestLoadWorker_Unified(t *testing.T) {
	// runtime.mongo.uri via a hierarchical flag; instanceID via the convenience
	// alias that maps onto tasks.instanceID.
	fs := pflag.NewFlagSet("worker", pflag.ContinueOnError)
	fs.String("runtime.mongo.uri", "", "")
	fs.String("instanceID", "", "")
	if err := fs.Parse([]string{"--runtime.mongo.uri", "mongodb://flag/worker", "--instanceID", "worker-1"}); err != nil {
		t.Fatal(err)
	}
	rc, rt, err := LoadWorker("", fs)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Mongo.URI != "mongodb://flag/worker" {
		t.Errorf("LoadWorker Mongo.URI = %q", rt.Mongo.URI)
	}
	if rt.InstanceID != "worker-1" || rc.Tasks.InstanceID != "worker-1" {
		t.Errorf("LoadWorker InstanceID = %q (tasks=%q)", rt.InstanceID, rc.Tasks.InstanceID)
	}
	if !rt.Worker.Enabled || !rt.RemoteConfig.Enabled {
		t.Errorf("worker switches: worker=%v remoteConfig=%v", rt.Worker.Enabled, rt.RemoteConfig.Enabled)
	}
}

func TestLoadWorker_RequiresInstanceID(t *testing.T) {
	yaml := "runtime:\n  mongo:\n    uri: \"mongodb://localhost/worker\"\n"
	if _, _, err := LoadWorker(writeFile(t, "worker.yaml", yaml), nil); err == nil {
		t.Fatal("expected error: worker without tasks.instanceID")
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
tasks:
  collection: custom_tasks
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
	if cc.Publish.TasksCollection != "custom_tasks" {
		t.Errorf("tasks.collection -> publish = %q", cc.Publish.TasksCollection)
	}
}

func TestLoadOperator_Unified(t *testing.T) {
	yaml := `
runtime:
  mongo:
    uri: "mongodb://localhost/op"
backfillFilter:
  table: user
`
	_, cc, err := LoadOperator(writeFile(t, "operator.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cc.BackfillFilter.Table != "user" {
		t.Errorf("backfillFilter.table = %q", cc.BackfillFilter.Table)
	}
	// Publish defaults must be filled even when tasks is omitted.
	if cc.Publish.TasksCollection != DefaultTasksCollection {
		t.Errorf("publish default = %q", cc.Publish.TasksCollection)
	}
}

// TestExampleRoleConfigsLoad ensures every shipped role example (max + min, in
// both YAML and JSON) parses and validates under its loader.
func TestExampleRoleConfigsLoad(t *testing.T) {
	report := []string{
		"../examples/config/report/report.max.yaml",
		"../examples/config/report/report.min.yaml",
		"../examples/config/report/report.max.json",
		"../examples/config/report/report.min.json",
	}
	for _, p := range report {
		t.Run(p, func(t *testing.T) {
			if _, _, err := LoadReport(p, nil); err != nil {
				t.Fatalf("LoadReport(%s): %v", p, err)
			}
		})
	}
	worker := []string{
		"../examples/config/worker/worker.max.yaml",
		"../examples/config/worker/worker.min.yaml",
		"../examples/config/worker/worker.max.json",
		"../examples/config/worker/worker.min.json",
	}
	for _, p := range worker {
		t.Run(p, func(t *testing.T) {
			if _, _, err := LoadWorker(p, nil); err != nil {
				t.Fatalf("LoadWorker(%s): %v", p, err)
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
	operator := []string{
		"../examples/config/operator/operator.max.yaml",
		"../examples/config/operator/operator.min.yaml",
		"../examples/config/operator/operator.max.json",
		"../examples/config/operator/operator.min.json",
	}
	for _, p := range operator {
		t.Run(p, func(t *testing.T) {
			if _, _, err := LoadOperator(p, nil); err != nil {
				t.Fatalf("LoadOperator(%s): %v", p, err)
			}
		})
	}
}
