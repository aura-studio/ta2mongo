package client

import (
	"testing"
	"time"

	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
)

func TestOptions_defaults_SetsMaxElapsedTimeAndBatchSize(t *testing.T) {
	var o Options
	o.defaults()

	if o.MaxElapsedTime != 10*time.Second {
		t.Fatalf("MaxElapsedTime=%v, want %v", o.MaxElapsedTime, 10*time.Second)
	}
	if o.BatchSize != 1000 {
		t.Fatalf("BatchSize=%d, want %d", o.BatchSize, 1000)
	}
}

func TestNew_ErrorsWhenURIEmpty(t *testing.T) {
	_, err := New(nil,
		WithURI(""),
	)
	if err == nil {
		t.Fatalf("expected error for empty URI")
	}
}

func TestMongoDBFromURI_DefaultDBWhenURINoDBInPath(t *testing.T) {
	db, err := daomongo.MongoDBFromURI("mongodb://localhost:27017")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if db != "tango" {
		t.Fatalf("db=%q, want %q", db, "tango")
	}
}
