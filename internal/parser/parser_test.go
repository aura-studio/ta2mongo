package parser

import "testing"

func TestSwapFilter_CompilesThenSwaps(t *testing.T) {
	p := New(nil)
	if !p.Filter().Empty() {
		t.Fatal("fresh parser should hold an empty (no-op) filter")
	}
	if err := p.SwapFilter([]string{`#type == "track"`}, nil); err != nil {
		t.Fatalf("SwapFilter: %v", err)
	}
	if p.Filter().Empty() {
		t.Fatal("filter should be non-empty after a successful swap")
	}
}

func TestSwapFilter_CompileFailureKeepsLastGood(t *testing.T) {
	p := New(nil)
	if err := p.SwapFilter([]string{`#type == "track"`}, nil); err != nil {
		t.Fatalf("initial SwapFilter: %v", err)
	}
	good := p.Filter().Current()

	// A malformed expression must return an error and leave the live filter
	// untouched (last-good preserved) — the parser-side half of the cfgsync
	// validate-before-swap guarantee.
	if err := p.SwapFilter([]string{`#type ===== "track"`}, nil); err == nil {
		t.Fatal("expected a compile error")
	}
	if p.Filter().Current() != good {
		t.Fatal("live filter changed despite a compile failure")
	}
}
