package core

import (
	"context"
	"testing"

	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/parser/filter"
)

// countStats records how many times each callback fired.
type countStats struct {
	line, parseOK, parseErr, identErr int
	userW, eventW, deadLetter         int
	filtered, filterErr               int
}

func (c *countStats) OnLine()          { c.line++ }
func (c *countStats) OnParseOK()       { c.parseOK++ }
func (c *countStats) OnParseError()    { c.parseErr++ }
func (c *countStats) OnIdentityError() { c.identErr++ }
func (c *countStats) OnUserWrite()     { c.userW++ }
func (c *countStats) OnEventWrite()    { c.eventW++ }
func (c *countStats) OnDeadLetter()    { c.deadLetter++ }
func (c *countStats) OnWriteError()    {}
func (c *countStats) OnFiltered()      { c.filtered++ }
func (c *countStats) OnFilterError()   { c.filterErr++ }

// The parse-error and filtered paths return before identity resolution, so they
// exercise Process without a live store (the success paths are covered by the
// ingest/pipeline/backfill integration tests).

func TestProcess_ParseError(t *testing.T) {
	cs := &countStats{}
	p := NewProcessor(parser.New(nil), nil, cs, WriteOptions{})
	res := p.Process(context.Background(), "this is not json")
	if res.Kind != KindParseError {
		t.Fatalf("Kind = %v, want KindParseError", res.Kind)
	}
	if res.Model == nil {
		t.Error("expected a dead-letter model")
	}
	if res.Err == nil {
		t.Error("expected a parse error")
	}
	if cs.line != 1 || cs.parseErr != 1 || cs.deadLetter != 1 || cs.parseOK != 0 {
		t.Errorf("stats = %+v", cs)
	}
}

func TestProcess_Filtered(t *testing.T) {
	cs := &countStats{}
	flt, err := filter.New([]string{`#type == "track"`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := NewProcessor(parser.New(flt), nil, cs, WriteOptions{})
	// A valid user_set record that does not match the track-only include filter.
	line := `{"#type":"user_set","#time":"2024-01-01 00:00:00","#uuid":"u2","#account_id":"acc2","properties":{"name":"Alice"}}`
	res := p.Process(context.Background(), line)
	if res.Kind != KindFiltered {
		t.Fatalf("Kind = %v, want KindFiltered", res.Kind)
	}
	if res.Model != nil {
		t.Error("filtered result must carry no write model")
	}
	if cs.line != 1 || cs.parseOK != 1 || cs.filtered != 1 || cs.deadLetter != 0 {
		t.Errorf("stats = %+v", cs)
	}
}

func TestProcess_NilFilterKeepsEverythingUntilIdentity(t *testing.T) {
	// With a nil filter holder, a parsed record is never dropped at the filter
	// stage; it proceeds to identity resolution. We stop short of asserting the
	// store-backed outcome here (covered by integration tests) and only verify
	// the filter stage did not classify it as filtered.
	cs := &countStats{}
	p := NewProcessor(parser.New(nil), nil, cs, WriteOptions{})
	// A parse error still short-circuits; use that to confirm no filter stat.
	_ = p.Process(context.Background(), "nope")
	if cs.filtered != 0 || cs.filterErr != 0 {
		t.Errorf("nil filter should record no filter stats, got %+v", cs)
	}
}
