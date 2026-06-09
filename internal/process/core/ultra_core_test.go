package core

import (
	"context"
	"strings"
	"testing"

	"github.com/aura-studio/tango/internal/parser"
)

// recordingStats is a StatsCollector that records how many times each callback
// fired, so the panic-recovery path can be asserted precisely.
type recordingStats struct {
	line, parseOK, parseErr, identErr int
	userW, eventW, deadLetter         int
	writeErr, filtered, filterErr     int
}

func (c *recordingStats) OnLine()          { c.line++ }
func (c *recordingStats) OnParseOK()       { c.parseOK++ }
func (c *recordingStats) OnParseError()    { c.parseErr++ }
func (c *recordingStats) OnIdentityError() { c.identErr++ }
func (c *recordingStats) OnUserWrite()     { c.userW++ }
func (c *recordingStats) OnEventWrite()    { c.eventW++ }
func (c *recordingStats) OnDeadLetter()    { c.deadLetter++ }
func (c *recordingStats) OnWriteError()    { c.writeErr++ }
func (c *recordingStats) OnFiltered()      { c.filtered++ }
func (c *recordingStats) OnFilterError()   { c.filterErr++ }

// TestUltraCore_ProcessRecoversPanicToDeadLetter covers CORE-6: a per-line panic
// (here forced by a valid line that parses + passes the nil filter and then hits
// identity resolution on a NIL store, panicking in Store.Identity) must be
// recovered into a KindParseError dead-letter Result rather than crashing the
// worker. Process must NOT propagate the panic, must produce a non-nil
// dead-letter model, and must record OnParseError + OnDeadLetter for the
// recovered line (in addition to the always-on OnLine and the OnParseOK that
// fired before the panic).
func TestUltraCore_ProcessRecoversPanicToDeadLetter(t *testing.T) {
	cs := &recordingStats{}
	// Nil store: ParseLine succeeds and the nil filter keeps the record, so
	// Process reaches p.store.Identity(), which dereferences the nil *dao.Store
	// and panics — exercising the deferred recover() path.
	p := NewProcessor(parser.New(nil), nil, cs)

	// A valid track line that parses cleanly and is not dropped by the (absent)
	// filter, guaranteeing we get past OnParseOK and into the panicking identity
	// step rather than short-circuiting at the parse-error branch.
	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"u1","#account_id":"acc1","#distinct_id":"did1","properties":{"ip":"1.2.3.4"}}`

	var res Result
	func() {
		// If the recover inside Process were missing, the panic would escape and
		// this anonymous func's own recover would catch it — failing the test
		// explicitly rather than crashing the whole test binary.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Process let a panic escape (CORE-6 regression): %v", r)
			}
		}()
		res = p.Process(context.Background(), line)
	}()

	if res.Kind != KindParseError {
		t.Fatalf("recovered panic: Kind = %v, want KindParseError", res.Kind)
	}
	if res.Model == nil {
		t.Error("recovered panic must carry a non-nil dead-letter write model")
	}
	if res.Err == nil {
		t.Fatal("recovered panic must carry a non-nil Err")
	}
	// processor.go wraps the recovered value as "panic processing line: %v".
	if got := res.Err.Error(); !strings.Contains(got, "panic processing line") {
		t.Errorf("Err = %q, want it to contain %q", got, "panic processing line")
	}

	// Stats accounting for the recovered line: OnLine fires first; OnParseOK
	// fires because the line parsed before the panic; the recover handler then
	// records exactly one OnParseError and one OnDeadLetter. No identity-error,
	// filter, or write stats should be recorded on this path.
	if cs.line != 1 {
		t.Errorf("OnLine count = %d, want 1", cs.line)
	}
	if cs.parseOK != 1 {
		t.Errorf("OnParseOK count = %d, want 1 (line parsed before panic)", cs.parseOK)
	}
	if cs.parseErr != 1 {
		t.Errorf("OnParseError count = %d, want 1 (recovered panic)", cs.parseErr)
	}
	if cs.deadLetter != 1 {
		t.Errorf("OnDeadLetter count = %d, want 1 (recovered panic)", cs.deadLetter)
	}
	if cs.identErr != 0 {
		t.Errorf("OnIdentityError count = %d, want 0 (panic, not a normal identity error)", cs.identErr)
	}
	if cs.userW != 0 || cs.eventW != 0 {
		t.Errorf("write counts = user:%d event:%d, want 0/0", cs.userW, cs.eventW)
	}
	if cs.filtered != 0 || cs.filterErr != 0 {
		t.Errorf("filter counts = filtered:%d filterErr:%d, want 0/0", cs.filtered, cs.filterErr)
	}
}

// TestUltraCore_NewProcessorNilStatsDefaultsToNoop confirms NewProcessor's
// documented contract that a nil StatsCollector is treated as a no-op: building
// a Processor with nil stats and forcing the panic-recovery path must not panic
// on the internal p.stats.* calls (which would happen if the nil were stored
// directly instead of being swapped for NoopStats).
func TestUltraCore_NewProcessorNilStatsDefaultsToNoop(t *testing.T) {
	p := NewProcessor(parser.New(nil), nil, nil)

	line := `{"#type":"track","#event_name":"login","#time":"2024-01-01 00:00:00","#uuid":"u1","#account_id":"acc1","#distinct_id":"did1","properties":{}}`

	var res Result
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("nil stats must default to NoopStats; got panic: %v", r)
			}
		}()
		res = p.Process(context.Background(), line)
	}()

	// Even with NoopStats the classification of the recovered panic is the same.
	if res.Kind != KindParseError {
		t.Fatalf("Kind = %v, want KindParseError", res.Kind)
	}
	if res.Model == nil {
		t.Error("expected a non-nil dead-letter model with nil-defaulted stats")
	}
}

// TestUltraCore_NoopStatsAllNoOp covers STAT-3: every NoopStats method is a
// callable no-op, and NoopStats satisfies the StatsCollector interface. Calling
// all ten callbacks must not panic.
func TestUltraCore_NoopStatsAllNoOp(t *testing.T) {
	// Compile-time + run-time proof that NoopStats implements StatsCollector.
	var sc StatsCollector = NoopStats{}

	// Each call is a no-op; the test passes as long as none panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NoopStats method panicked: %v", r)
		}
	}()

	sc.OnLine()
	sc.OnParseOK()
	sc.OnParseError()
	sc.OnIdentityError()
	sc.OnUserWrite()
	sc.OnEventWrite()
	sc.OnDeadLetter()
	sc.OnWriteError()
	sc.OnFiltered()
	sc.OnFilterError()

	// Calling each one twice exercises that they hold no per-call state and are
	// idempotent no-ops.
	sc.OnLine()
	sc.OnLine()

	// NoopStats has no observable state to assert; reaching here without a panic
	// is the contract. A zero-value NoopStats is usable directly, too.
	var direct NoopStats
	direct.OnLine()
	direct.OnWriteError()
}
