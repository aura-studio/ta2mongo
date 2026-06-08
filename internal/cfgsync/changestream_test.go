package cfgsync

import (
	"errors"
	"strings"
	"testing"
)

// TestUnsupportedTopologyError asserts the degradation contract: when a change
// stream cannot be opened (standalone mongod, DocumentDB without
// modifyChangeStreams, Elastic Cluster), the failure is surfaced with a clear,
// actionable message that names backend=poll and wraps the underlying driver
// error rather than swallowing it.
func TestUnsupportedTopologyError(t *testing.T) {
	cause := errors.New("The $changeStream stage is only supported on replica sets")
	err := unsupportedTopologyError(cause)

	if err == nil {
		t.Fatal("expected a non-nil error")
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error does not wrap the cause: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, BackendPoll) {
		t.Fatalf("error does not point at backend=%s: %q", BackendPoll, msg)
	}
	if !strings.Contains(msg, "change stream unavailable") {
		t.Fatalf("error does not explain the cause: %q", msg)
	}
}
