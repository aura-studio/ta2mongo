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

func TestLoadDaemon_ClusterMode(t *testing.T) {
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
    remote:
      syncInterval: "2m"
`
	_, rt, err := LoadDaemon(writeFile(t, "daemon.yaml", yaml), nil, DaemonModeCluster)
	if err != nil {
		t.Fatal(err)
	}
	// Cluster mode turns on the remote-config sync switch.
	if !rt.RemoteConfig.Enabled {
		t.Errorf("cluster mode should enable remote-config sync")
	}
	if rt.RemoteConfig.SyncInterval.String() != "2m0s" {
		t.Errorf("report.filter.remote.syncInterval = %v", rt.RemoteConfig.SyncInterval)
	}
	if len(rt.Filter.Include) != 1 || rt.Filter.Include[0] != `#type == "track"` {
		t.Errorf("report.filter.local -> runtime Filter = %v", rt.Filter.Include)
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
	// Standalone keeps the control plane off.
	if rt.RemoteConfig.Enabled {
		t.Errorf("standalone should not enable remote-config sync")
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

func TestLoadDaemon_RequiresMongoURI(t *testing.T) {
	yaml := `
report:
  source:
    logPattern: ["/tmp/.*\\.log"]
`
	if _, _, err := LoadDaemon(writeFile(t, "daemon.yaml", yaml), nil, DaemonModeStandalone); err == nil {
		t.Fatal("expected error: missing generic.mongo.uri")
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
	if err := fs.Parse([]string{
		"--config", "/ignored",
		"--generic.mongo.uri", "mongodb://flag/db",
		"--generic.logging.level", "debug",
	}); err != nil {
		t.Fatal(err)
	}
	// report.source.logPattern can't come from a flag, so supply it via a file.
	path := writeFile(t, "daemon.yaml", "report:\n  source:\n    logPattern: [\"/tmp/x.log\"]\n")
	_, rt, err := LoadDaemon(path, fs, DaemonModeStandalone)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Mongo.URI != "mongodb://flag/db" {
		t.Errorf("generic.mongo.uri flag = %q", rt.Mongo.URI)
	}
	if rt.Logging.Level != "debug" {
		t.Errorf("generic.logging.level flag = %q", rt.Logging.Level)
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
		{"../examples/config/cluster/cluster.min.yaml", DaemonModeCluster},
		{"../examples/config/cluster/cluster.max.yaml", DaemonModeCluster},
		{"../examples/config/cluster/cluster.min.json", DaemonModeCluster},
		{"../examples/config/cluster/cluster.max.json", DaemonModeCluster},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			if _, _, err := LoadDaemon(c.path, nil, c.mode); err != nil {
				t.Fatalf("LoadDaemon(%s, %s): %v", c.path, c.mode, err)
			}
		})
	}
}
