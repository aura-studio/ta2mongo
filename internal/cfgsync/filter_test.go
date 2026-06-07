package cfgsync

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/parser"
)

func TestRegisterFilter_SwapsLiveFilter(t *testing.T) {
	p := parser.New(nil)
	if !p.Filter().Empty() {
		t.Fatal("fresh parser should have an empty (no-op) filter")
	}
	reg := NewRegistry()
	RegisterFilter(reg, p)

	doc := bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}}
	if err := reg.Apply(doc); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if p.Filter().Empty() {
		t.Fatal("filter should have been swapped to a non-empty rule set")
	}
}

func TestRegisterFilter_BadExpressionKeepsLastGood(t *testing.T) {
	p := parser.New(nil)
	reg := NewRegistry()
	RegisterFilter(reg, p)

	// First, install a good filter.
	if err := reg.Apply(bson.M{"filter": bson.M{"include": bson.A{`#type == "track"`}}}); err != nil {
		t.Fatalf("good apply: %v", err)
	}
	good := p.Filter().Current()

	// Then a filter that does not compile must be rejected and the live filter
	// must stay at the last-good value.
	err := reg.Apply(bson.M{"filter": bson.M{"include": bson.A{`#type ===== "track"`}}})
	if err == nil {
		t.Fatal("expected a compile error for a malformed expression")
	}
	if p.Filter().Current() != good {
		t.Fatal("live filter changed despite a compile failure (last-good not preserved)")
	}
}

func TestRegisterFilter_NonStringRuleRejected(t *testing.T) {
	p := parser.New(nil)
	reg := NewRegistry()
	RegisterFilter(reg, p)
	if err := reg.Apply(bson.M{"filter": bson.M{"include": bson.A{42}}}); err == nil {
		t.Fatal("expected an error for a non-string filter rule")
	}
}

func TestToStringSlice(t *testing.T) {
	if got, err := toStringSlice(nil); err != nil || got != nil {
		t.Errorf("nil: %v / %v", got, err)
	}
	if got, err := toStringSlice(bson.A{"a", "b"}); err != nil || len(got) != 2 || got[0] != "a" {
		t.Errorf("bson.A: %v / %v", got, err)
	}
	if got, err := toStringSlice([]string{"x"}); err != nil || len(got) != 1 {
		t.Errorf("[]string: %v / %v", got, err)
	}
	if _, err := toStringSlice(bson.A{1}); err == nil {
		t.Error("expected error for non-string element")
	}
	if _, err := toStringSlice("scalar"); err == nil {
		t.Error("expected error for non-array value")
	}
}
