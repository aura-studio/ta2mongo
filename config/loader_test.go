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

func TestLoadDaemon_YAML(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost/tango"
source:
  logPattern: ["/tmp/.*\\.log"]
reportFilter:
  include: ['#type == "track"']
agent:
  enabled: true
  instanceID: "node-1"
  leaseDuration: "2m"
`
	dc, rt, err := LoadDaemon(writeFile(t, "daemon.yaml", yaml), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !dc.Agent.Enabled || dc.Agent.InstanceID != "node-1" {
		t.Errorf("agent block = %+v", dc.Agent)
	}
	if rt.InstanceID != "node-1" {
		t.Errorf("runtime InstanceID = %q", rt.InstanceID)
	}
	if len(rt.Filter.Include) != 1 || rt.Filter.Include[0] != `#type == "track"` {
		t.Errorf("reportFilter -> runtime Filter = %v", rt.Filter.Include)
	}
	if rt.Agent.LeaseDuration.String() != "2m0s" {
		t.Errorf("leaseDuration = %v", rt.Agent.LeaseDuration)
	}
}

func TestLoadDaemon_AgentEnabledRequiresInstanceID(t *testing.T) {
	yaml := `
mongo:
  uri: "mongodb://localhost/tango"
agent:
  enabled: true
`
	if _, _, err := LoadDaemon(writeFile(t, "daemon.yaml", yaml), nil); err == nil {
		t.Fatal("expected error: agent.enabled without instanceID")
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
// validate under the daemon schema (both YAML and JSON).
func TestExampleDaemonConfigsLoad(t *testing.T) {
	for _, p := range []string{"../examples/config/daemon/daemon.yaml", "../examples/config/daemon/daemon.json"} {
		t.Run(p, func(t *testing.T) {
			if _, _, err := LoadDaemon(p, nil); err != nil {
				t.Fatalf("LoadDaemon(%s): %v", p, err)
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
