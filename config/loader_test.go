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

func TestLoadDaemon_AgentMode(t *testing.T) {
	yaml := `
generic:
  mongo:
    uri: "mongodb://localhost/tango"
report:
  source:
    logPattern: ["/tmp/.*\\.log"]
  filter:
    local:
      include: ['#type == "track"']
agent:
  instanceID: "node-1"
  leaseDuration: "2m"
`
	dc, rt, err := LoadDaemon(writeFile(t, "daemon.yaml", yaml), nil, DaemonModeAgent)
	if err != nil {
		t.Fatal(err)
	}
	if dc.Agent.InstanceID != "node-1" {
		t.Errorf("agent block = %+v", dc.Agent)
	}
	if rt.InstanceID != "node-1" {
		t.Errorf("runtime InstanceID = %q", rt.InstanceID)
	}
	// Agent mode turns on the control-plane switches.
	if !rt.Agent.Enabled || !rt.RemoteConfig.Enabled {
		t.Errorf("agent mode switches: agent=%v remoteConfig=%v", rt.Agent.Enabled, rt.RemoteConfig.Enabled)
	}
	if len(rt.Filter.Include) != 1 || rt.Filter.Include[0] != `#type == "track"` {
		t.Errorf("report.filter -> runtime Filter = %v", rt.Filter.Include)
	}
	if rt.Agent.LeaseDuration.String() != "2m0s" {
		t.Errorf("leaseDuration = %v", rt.Agent.LeaseDuration)
	}
}

func TestLoadDaemon_StandaloneMode(t *testing.T) {
	yaml := `
generic:
  mongo:
    uri: "mongodb://localhost/tango"
report:
  source:
    logPattern: ["/tmp/.*\\.log"]
`
	_, rt, err := LoadDaemon(writeFile(t, "daemon.yaml", yaml), nil, DaemonModeStandalone)
	if err != nil {
		t.Fatal(err)
	}
	// Standalone leaves the control plane off, regardless of file contents.
	if rt.Agent.Enabled || rt.RemoteConfig.Enabled {
		t.Errorf("standalone switches: agent=%v remoteConfig=%v", rt.Agent.Enabled, rt.RemoteConfig.Enabled)
	}
}

func TestLoadDaemon_AgentModeRequiresInstanceID(t *testing.T) {
	yaml := `
generic:
  mongo:
    uri: "mongodb://localhost/tango"
report:
  source:
    logPattern: ["/tmp/.*\\.log"]
`
	if _, _, err := LoadDaemon(writeFile(t, "daemon.yaml", yaml), nil, DaemonModeAgent); err == nil {
		t.Fatal("expected error: agent mode without instanceID")
	}
}

func TestLoadDaemon_RequiresLogPattern(t *testing.T) {
	yaml := `
generic:
  mongo:
    uri: "mongodb://localhost/tango"
`
	if _, _, err := LoadDaemon(writeFile(t, "daemon.yaml", yaml), nil, DaemonModeStandalone); err == nil {
		t.Fatal("expected error: standalone without logPattern")
	}
}

func TestLoadDaemon_UnknownMode(t *testing.T) {
	if _, _, err := LoadDaemon("", nil, "bogus"); err == nil {
		t.Fatal("expected error: unknown daemon mode")
	}
}

// TestLoadDaemon_HierarchicalFlags verifies viper-native flag binding: a flag
// named after the full config key sets that key directly (no alias table), and
// the --config flag is not treated as a config key.
func TestLoadDaemon_HierarchicalFlags(t *testing.T) {
	fs := pflag.NewFlagSet("daemon", pflag.ContinueOnError)
	fs.String("config", "", "")
	fs.String("generic.mongo.uri", "", "")
	fs.String("generic.logging.level", "", "")
	fs.String("agent.instanceID", "", "")
	if err := fs.Parse([]string{
		"--config", "/ignored",
		"--generic.mongo.uri", "mongodb://flag/db",
		"--generic.logging.level", "debug",
		"--agent.instanceID", "node-flag",
	}); err != nil {
		t.Fatal(err)
	}
	// report.source.logPattern can't come from a flag, so supply it via a file.
	path := writeFile(t, "daemon.yaml", "report:\n  source:\n    logPattern: [\"/tmp/x.log\"]\n")
	_, rt, err := LoadDaemon(path, fs, DaemonModeAgent)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Mongo.URI != "mongodb://flag/db" {
		t.Errorf("generic.mongo.uri flag = %q", rt.Mongo.URI)
	}
	if rt.Logging.Level != "debug" {
		t.Errorf("generic.logging.level flag = %q", rt.Logging.Level)
	}
	if rt.InstanceID != "node-flag" {
		t.Errorf("agent.instanceID flag = %q", rt.InstanceID)
	}
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
	if rt.Agent.Enabled {
		t.Error("LoadReport unexpectedly enabled task worker")
	}
	if !rt.RemoteConfig.Enabled {
		t.Error("LoadReport did not honour remoteConfig.enabled")
	}
	if len(rc.Report.Source.LogPattern) != 1 {
		t.Errorf("RoleConfig logPattern = %v", rc.Report.Source.LogPattern)
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
	if !rt.Agent.Enabled || !rt.RemoteConfig.Enabled {
		t.Errorf("worker switches: agent=%v remoteConfig=%v", rt.Agent.Enabled, rt.RemoteConfig.Enabled)
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

func TestLoadClient_JSON(t *testing.T) {
	json := `{
  "mongo": {"uri": "mongodb://localhost/tango"},
  "stringUpload": {"batchSize": 500},
  "backfillFilter": {"table": "user"},
  "server": {"addr": ":9999"}
}`
	cc, err := LoadClient(writeFile(t, "client.json", json), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cc.StringUpload.BatchSize != 500 {
		t.Errorf("stringUpload.batchSize = %d", cc.StringUpload.BatchSize)
	}
	if cc.BackfillFilter.Table != "user" {
		t.Errorf("backfillFilter.table = %q", cc.BackfillFilter.Table)
	}
	if cc.Server.Addr != ":9999" {
		t.Errorf("server.addr = %q", cc.Server.Addr)
	}
	if cc.Publish.TasksCollection != DefaultTasksCollection {
		t.Errorf("publish default = %q", cc.Publish.TasksCollection)
	}
	// Section runtime builders.
	if rt := cc.BackfillRuntime(); rt.BackfillFilter.Table != "user" || rt.Mode != ModeBackfill {
		t.Errorf("BackfillRuntime = %+v", rt.BackfillFilter)
	}
}

// TestExampleDaemonConfigsLoad ensures the shipped daemon examples parse and
// validate under their respective run mode (full, minimal, and JSON variants).
func TestExampleDaemonConfigsLoad(t *testing.T) {
	cases := []struct{ path, mode string }{
		{"../examples/config/standalone/standalone.min.yaml", DaemonModeStandalone},
		{"../examples/config/standalone/standalone.max.yaml", DaemonModeStandalone},
		{"../examples/config/standalone/standalone.min.json", DaemonModeStandalone},
		{"../examples/config/standalone/standalone.max.json", DaemonModeStandalone},
		{"../examples/config/agent/agent.min.yaml", DaemonModeAgent},
		{"../examples/config/agent/agent.max.yaml", DaemonModeAgent},
		{"../examples/config/agent/agent.min.json", DaemonModeAgent},
		{"../examples/config/agent/agent.max.json", DaemonModeAgent},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if _, _, err := LoadDaemon(c.path, nil, c.mode); err != nil {
				t.Fatalf("LoadDaemon(%s, %s): %v", c.path, c.mode, err)
			}
		})
	}
}

// TestExampleClientConfigsLoad ensures the shipped client examples parse under
// the client schema (both YAML and JSON) and that section runtime builders work.
func TestExampleClientConfigsLoad(t *testing.T) {
	for _, p := range []string{"../examples/config/client/client.yaml", "../examples/config/client/client.json"} {
		t.Run(p, func(t *testing.T) {
			cc, err := LoadClient(p, nil)
			if err != nil {
				t.Fatalf("LoadClient(%s): %v", p, err)
			}
			if cc.BackfillFilter.Table != BackfillTableEvent {
				t.Errorf("backfillFilter.table = %q", cc.BackfillFilter.Table)
			}
			// Backfill runtime must validate (table + filter compile).
			rt := cc.BackfillRuntime()
			if err := rt.Validate(); err != nil {
				t.Errorf("BackfillRuntime invalid: %v", err)
			}
		})
	}
}

// TestExampleRoleConfigsLoad ensures the shipped unified role examples
// (report/worker/gateway/operator) parse and validate under their loaders.
func TestExampleRoleConfigsLoad(t *testing.T) {
	if _, _, err := LoadReport("../examples/config/report/report.yaml", nil); err != nil {
		t.Errorf("LoadReport(report.yaml): %v", err)
	}
	if _, _, err := LoadWorker("../examples/config/worker/worker.yaml", nil); err != nil {
		t.Errorf("LoadWorker(worker.yaml): %v", err)
	}
	if _, _, err := LoadGateway("../examples/config/gateway/gateway.yaml", nil); err != nil {
		t.Errorf("LoadGateway(gateway.yaml): %v", err)
	}
	if _, _, err := LoadOperator("../examples/config/operator/operator.yaml", nil); err != nil {
		t.Errorf("LoadOperator(operator.yaml): %v", err)
	}
}

func TestLoadClient_EnvOverride(t *testing.T) {
	os.Setenv("TANGO_MONGO_URI", "mongodb://env/db")
	defer os.Unsetenv("TANGO_MONGO_URI")
	cc, err := LoadClient("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cc.Mongo.URI != "mongodb://env/db" {
		t.Errorf("env override Mongo.URI = %q", cc.Mongo.URI)
	}
}
