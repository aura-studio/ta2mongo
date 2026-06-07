package cfgsync

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/parser"
)

func TestRegistryApply_RoutesAllowlistedSubtree(t *testing.T) {
	got := map[string]bson.M{}
	reg := NewRegistry()
	reg.Register("filter", func(sub bson.M) error { got["filter"] = sub; return nil })

	doc := bson.M{
		"_id":     "filter",
		"version": int64(3),
		"filter":  bson.M{"include": bson.A{`#type == "track"`}},
	}
	if err := reg.Apply(doc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := got["filter"]; !ok {
		t.Fatal("filter applier was not invoked")
	}
}

func TestRegistryApply_RejectsOffAllowlist(t *testing.T) {
	reg := NewRegistry() // nothing registered
	err := reg.Apply(bson.M{"version": int64(1), "dao": bson.M{"mongo": bson.M{}}})
	if err == nil {
		t.Fatal("expected an error for a subtree off the allowlist")
	}
}

func TestRegistryApply_SkipsReservedFields(t *testing.T) {
	reg := NewRegistry() // no appliers, but reserved-only doc must not error
	if err := reg.Apply(bson.M{"_id": "filter", "version": int64(9)}); err != nil {
		t.Fatalf("reserved-only doc should be a no-op, got: %v", err)
	}
}

func TestRegistryAllows(t *testing.T) {
	reg := NewRegistry()
	if reg.Allows(SubtreeFilter) {
		t.Fatal("empty registry should allow nothing")
	}
	RegisterFilter(reg, parser.New(nil))
	if !reg.Allows(SubtreeFilter) {
		t.Fatal("RegisterFilter should put filter on the allowlist")
	}
}

func TestAsSubDocument(t *testing.T) {
	if _, err := asSubDocument("filter", bson.M{"a": 1}); err != nil {
		t.Errorf("bson.M: %v", err)
	}
	m, err := asSubDocument("filter", bson.D{{Key: "a", Value: 1}})
	if err != nil || m["a"] != 1 {
		t.Errorf("bson.D: %v / %v", m, err)
	}
	if _, err := asSubDocument("filter", "not a doc"); err == nil {
		t.Error("expected error for non-document value")
	}
}
