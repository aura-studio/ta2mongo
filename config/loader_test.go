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

func TestLoadDaemon_AgentMode(t *testing.T) {
	yaml := `
common:
  mongo:
    uri: "mongodb://localhost/tango"
report:
  source:
    logPattern: ["/tmp/.*\\.log"]
  filter:
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
common:
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
common:
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
common:
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
// validate under the daemon schema (both YAML and JSON) in both run modes.
func TestExampleDaemonConfigsLoad(t *testing.T) {
	for _, p := range []string{"../examples/config/daemon/daemon.yaml", "../examples/config/daemon/daemon.json"} {
		for _, mode := range []string{DaemonModeStandalone, DaemonModeAgent} {
			t.Run(p+"/"+mode, func(t *testing.T) {
				if _, _, err := LoadDaemon(p, nil, mode); err != nil {
					t.Fatalf("LoadDaemon(%s, %s): %v", p, mode, err)
				}
			})
		}
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
