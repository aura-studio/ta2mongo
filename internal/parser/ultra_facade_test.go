package parser

// Ultra tests for FILT-12 (doc/ultra_test.md §3): the parser façade re-exports
// (Record / RecordCategory / CategoryUser / CategoryEvent / EnvelopeKeys) must
// be the very same types and values as parser/talog's, and the reporting
// filter must be fully reachable through Parser.Filter()/SwapFilter alone.
//
// This file deliberately does NOT import internal/parser/filter: that the
// filter-flow test below compiles at all is itself the FILT-12 proof that
// consumers never need that subpackage. talog IS imported — comparing the
// façade names against the originals is exactly what FILT-12 asks for.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aura-studio/tango/internal/parser/talog"
)

// Compile-time identity proofs: plain assignment with NO conversion, in both
// directions. These declarations only compile when the façade names are true
// aliases (type Record = talog.Record), not distinct defined types.
var (
	_ = func(r Record) talog.Record { return r }
	_ = func(r talog.Record) Record { return r }
	_ = func(p *Record) *talog.Record { return p }
	_ = func(p *talog.Record) *Record { return p }
	_ = func(c RecordCategory) talog.RecordCategory { return c }
	_ = func(c talog.RecordCategory) RecordCategory { return c }
)

func TestUltraFacade_FILT12_TypeIdentityWithTalog(t *testing.T) {
	// Runtime identity: an alias and its origin are one reflect.Type.
	if rt, tt := reflect.TypeOf(Record{}), reflect.TypeOf(talog.Record{}); rt != tt {
		t.Fatalf("parser.Record (%v) and talog.Record (%v) are distinct types", rt, tt)
	}
	rt := reflect.TypeOf(Record{})
	const talogPkg = "github.com/aura-studio/tango/internal/parser/talog"
	if rt.PkgPath() != talogPkg {
		t.Fatalf("parser.Record reflects PkgPath %q, want %q (true alias resolves to the origin package)",
			rt.PkgPath(), talogPkg)
	}
	if rt.Name() != "Record" {
		t.Fatalf("parser.Record reflects Name %q, want %q", rt.Name(), "Record")
	}

	ct := reflect.TypeOf(RecordCategory(0))
	if ct != reflect.TypeOf(talog.RecordCategory(0)) {
		t.Fatalf("parser.RecordCategory (%v) and talog.RecordCategory (%v) are distinct types",
			ct, reflect.TypeOf(talog.RecordCategory(0)))
	}
	if ct.Kind() != reflect.Int {
		t.Fatalf("RecordCategory kind = %v, want int", ct.Kind())
	}

	// Constant identity AND exact iota order from talog/record.go:
	// CategoryUser = iota (0), CategoryEvent (1).
	if CategoryUser != talog.CategoryUser {
		t.Fatalf("parser.CategoryUser (%d) != talog.CategoryUser (%d)", CategoryUser, talog.CategoryUser)
	}
	if CategoryEvent != talog.CategoryEvent {
		t.Fatalf("parser.CategoryEvent (%d) != talog.CategoryEvent (%d)", CategoryEvent, talog.CategoryEvent)
	}
	if int(CategoryUser) != 0 || int(CategoryEvent) != 1 {
		t.Fatalf("category values = (%d, %d), want (0, 1)", int(CategoryUser), int(CategoryEvent))
	}
}

func TestUltraFacade_FILT12_EnvelopeKeysIdentity(t *testing.T) {
	// Exact values and order, as documented in talog/record.go.
	want := []string{"msg", "message", "log"}
	if !reflect.DeepEqual(EnvelopeKeys, want) {
		t.Fatalf("parser.EnvelopeKeys = %v, want %v", EnvelopeKeys, want)
	}
	if !reflect.DeepEqual(talog.EnvelopeKeys, want) {
		t.Fatalf("talog.EnvelopeKeys = %v, want %v", talog.EnvelopeKeys, want)
	}
	// Not just equal — the SAME slice: parser.EnvelopeKeys is initialised from
	// talog.EnvelopeKeys, so both headers share one backing array.
	if len(EnvelopeKeys) != len(talog.EnvelopeKeys) || &EnvelopeKeys[0] != &talog.EnvelopeKeys[0] {
		t.Fatal("parser.EnvelopeKeys must share talog.EnvelopeKeys' backing array (re-export, not a copy)")
	}
}

func TestUltraFacade_FILT12_ParseLineProducesAliasRecord(t *testing.T) {
	p := New(nil)

	// ParseLine is promoted from the embedded *talog.Parser; its *Record
	// assigns to *talog.Record without conversion (alias identity in action).
	rec, err := p.ParseLine(`{"#type":"track","#uuid":"u-1","#time":"2026-06-10 12:00:00","#event_name":"login","#account_id":"acc-1","properties":{"level":7}}`)
	if err != nil {
		t.Fatalf("ParseLine(track): %v", err)
	}
	var tr *talog.Record = rec
	if tr.Type != "track" || tr.UUID != "u-1" || tr.AccountID != "acc-1" {
		t.Fatalf("record = {Type:%q UUID:%q AccountID:%q}, want {track u-1 acc-1}", tr.Type, tr.UUID, tr.AccountID)
	}
	if got := rec.Category(); got != CategoryEvent {
		t.Fatalf("track record Category() = %d, want parser.CategoryEvent (%d)", got, CategoryEvent)
	}
	// talog flattening is visible through the façade: properties.* promoted to
	// the root and the "properties" key removed.
	if got, ok := rec.Doc["level"].(float64); !ok || got != 7 {
		t.Fatalf("Doc[\"level\"] = %v (%T), want 7 (float64, promoted from properties)", rec.Doc["level"], rec.Doc["level"])
	}
	if _, ok := rec.Doc["properties"]; ok {
		t.Fatal(`Doc must not retain the "properties" key after flattening`)
	}

	urec, err := p.ParseLine(`{"#type":"user_set","#uuid":"u-2","#time":"2026-06-10 12:00:00","#distinct_id":"d-1"}`)
	if err != nil {
		t.Fatalf("ParseLine(user_set): %v", err)
	}
	if got := urec.Category(); got != CategoryUser {
		t.Fatalf("user_set record Category() = %d, want parser.CategoryUser (%d)", got, CategoryUser)
	}
}

func TestUltraFacade_FILT12_FilterReachableWithoutFilterImport(t *testing.T) {
	p := New(nil)

	// Filter() hands back the holder; its type is inferred, so no
	// parser/filter import is needed anywhere in this file (compile-level
	// proof of the façade boundary).
	h := p.Filter()
	if h == nil {
		t.Fatal("Filter() returned a nil holder")
	}
	if !h.Empty() {
		t.Fatal("fresh parser must hold an empty (no-op) filter")
	}
	if h.Current() != nil {
		t.Fatal("fresh parser's active filter must be nil (no-op keeps everything)")
	}
	keep, err := h.Keep(map[string]any{"#type": "track"})
	if err != nil || !keep {
		t.Fatalf("no-op Keep = (%v, %v), want (true, nil)", keep, err)
	}

	if err := p.SwapFilter([]string{`#type == "track"`}, []string{`#event_name == "debug"`}); err != nil {
		t.Fatalf("SwapFilter: %v", err)
	}
	if p.Filter() != h {
		t.Fatal("Filter() must return the same stable holder across swaps (the swap happens inside the holder)")
	}
	if h.Empty() {
		t.Fatal("holder must report non-empty after a successful swap")
	}
	if h.Current() == nil {
		t.Fatal("active filter must be non-nil after a successful swap")
	}

	cases := []struct {
		name string
		env  map[string]any
		want bool
	}{
		{"include hit", map[string]any{"#type": "track", "#event_name": "login"}, true},
		{"include miss", map[string]any{"#type": "user_set"}, false},
		{"exclude hit", map[string]any{"#type": "track", "#event_name": "debug"}, false},
	}
	for _, c := range cases {
		got, err := h.Keep(c.env)
		if err != nil {
			t.Fatalf("%s: Keep error: %v", c.name, err)
		}
		if got != c.want {
			t.Fatalf("%s: Keep(%v) = %v, want %v", c.name, c.env, got, c.want)
		}
	}

	// Compile failure: error names the offending expression and the live
	// filter is untouched (last-good — the parser-side cfgsync guarantee).
	goodFilter := h.Current()
	err = p.SwapFilter([]string{`#type ====`}, nil)
	if err == nil {
		t.Fatal("SwapFilter with a malformed expression must return a compile error")
	}
	if !strings.Contains(err.Error(), `filter: compile include[0] "#type ===="`) {
		t.Fatalf("compile error %q must name the offending include expression", err.Error())
	}
	if h.Current() != goodFilter {
		t.Fatal("live filter changed despite a failed swap (last-good violated)")
	}
}
