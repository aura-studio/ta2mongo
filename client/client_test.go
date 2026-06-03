package client

import (
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"rocket-nano/tools/tango/config"
)

func TestOptions_defaults_SetsMaxElapsedTimeAndBatchSizeAndLogger(t *testing.T) {
	var o Options
	o.defaults()

	if o.MaxElapsedTime != 10*time.Second {
		t.Fatalf("MaxElapsedTime=%v, want %v", o.MaxElapsedTime, 10*time.Second)
	}
	if o.BatchSize != 1000 {
		t.Fatalf("BatchSize=%d, want %d", o.BatchSize, 1000)
	}
	if o.Logger == nil {
		t.Fatalf("Logger is nil; expected default logger")
	}
	if got := o.Logger.GetLevel(); got != logrus.InfoLevel {
		t.Fatalf("Logger level=%v, want %v", got, logrus.InfoLevel)
	}
}

func TestOptions_defaults_PreservesProvidedLogger(t *testing.T) {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)

	o := Options{
		Logger: l,
	}
	o.defaults()

	if o.Logger != l {
		t.Fatalf("logger pointer changed; expected the provided logger to be preserved")
	}
	if got := o.Logger.GetLevel(); got != logrus.DebugLevel {
		t.Fatalf("Logger level=%v, want %v", got, logrus.DebugLevel)
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
	db, err := config.MongoDBFromURI("mongodb://localhost:27017")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if db != "tango" {
		t.Fatalf("db=%q, want %q", db, "tango")
	}
}
