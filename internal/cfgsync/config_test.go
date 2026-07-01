package cfgsync

import (
	"testing"
	"time"
)

func TestConfigApplyDefaults(t *testing.T) {
	var c Config
	c.ApplyDefaults()

	if c.Backend != BackendPoll {
		t.Errorf("backend = %q, want %q", c.Backend, BackendPoll)
	}
	if c.DocumentID != DefaultDocumentID {
		t.Errorf("documentID = %q, want %q", c.DocumentID, DefaultDocumentID)
	}
	if c.PollInterval != DefaultPollInterval {
		t.Errorf("pollInterval = %v, want %v", c.PollInterval, DefaultPollInterval)
	}
	if c.ReconcileInterval != DefaultReconcileInterval {
		t.Errorf("reconcileInterval = %v, want %v", c.ReconcileInterval, DefaultReconcileInterval)
	}
	if c.Enabled {
		t.Error("enabled should default to false")
	}
}

func TestConfigApplyDefaultsPreservesSetValues(t *testing.T) {
	c := Config{
		Enabled:      true,
		Backend:      BackendChangeStream,
		DocumentID:   "custom",
		PollInterval: 2 * time.Second,
	}
	c.ApplyDefaults()
	if !c.Enabled || c.Backend != BackendChangeStream || c.DocumentID != "custom" ||
		c.PollInterval != 2*time.Second {
		t.Fatalf("ApplyDefaults clobbered set values: %+v", c)
	}
	// reconcileInterval was unset → defaulted.
	if c.ReconcileInterval != DefaultReconcileInterval {
		t.Errorf("reconcileInterval = %v, want default", c.ReconcileInterval)
	}
}

func TestConfigValidate(t *testing.T) {
	for _, b := range []string{BackendPoll, BackendChangeStream} {
		c := &Config{Backend: b}
		if err := c.Validate(); err != nil {
			t.Errorf("Validate(%q): %v", b, err)
		}
	}
	if err := (&Config{Backend: "nope"}).Validate(); err == nil {
		t.Error("expected error for unknown backend")
	}
}
