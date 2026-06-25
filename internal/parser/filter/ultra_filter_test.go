package filter

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// FILT-7: an expression whose result is not a bool.
//
// New() compiles every expression with expr.AsBool(). expr-lang performs
// static type checking, so an expression with a statically-known non-bool type
// (e.g. the integer expression "1 + 1", or a bare string literal) is rejected
// at COMPILE time inside New(), surfacing as the indexed compile error from
// compileAll ("filter: compile include[i] ...").
//
// The runtime "did not return bool" path (evalBool) is only reachable when the
// static type is unknown (e.g. an undefined variable under
// AllowUndefinedVariables that resolves to a non-bool at run time). We cover
// both: the static compile error for "1 + 1", and the runtime non-bool error
// for an undefined-variable expression.
// ---------------------------------------------------------------------------

// TestUltraFilt7_NonBoolIntExprCompileError verifies that an include rule whose
// value is a numeric expression ("1 + 1") fails at New() with a compile error
// that names the offending include index and the original source.
func TestUltraFilt7_NonBoolIntExprCompileError(t *testing.T) {
	f, err := New([]string{"1 + 1"}, nil)
	if err == nil {
		t.Fatalf("expected compile error for non-bool include expr, got filter=%+v", f)
	}
	if f != nil {
		t.Errorf("expected nil *Filter on compile error, got %+v", f)
	}
	msg := err.Error()
	// compileAll wraps with kind+index+source.
	if !strings.Contains(msg, "include[0]") {
		t.Errorf("error should name offending include index, got: %q", msg)
	}
	if !strings.Contains(msg, "1 + 1") {
		t.Errorf("error should echo the offending source, got: %q", msg)
	}
}

// TestUltraFilt7_NonBoolExcludeCompileError verifies the same for the exclude
// side: a non-bool exclude expression is rejected at New() with an "exclude[i]"
// compile error.
func TestUltraFilt7_NonBoolExcludeCompileError(t *testing.T) {
	_, err := New(nil, []string{"2 * 3"})
	if err == nil {
		t.Fatalf("expected compile error for non-bool exclude expr")
	}
	if !strings.Contains(err.Error(), "exclude[0]") {
		t.Errorf("error should name offending exclude index, got: %q", err.Error())
	}
}

// TestUltraFilt7_RuntimeNonBoolTreatedAsNotMatched exercises the runtime branch
// of Keep: an undefined variable (allowed via AllowUndefinedVariables) has an
// unknown static type, so New() compiles it; at run time the value resolves to
// a non-bool (an int). expr-lang's AsBool() coercion enforces the bool check
// inside the VM, so expr.Run itself returns an "invalid operation: bool(int)"
// error (the evalBool out.(bool) guard is therefore not the surfacing path in
// this expr-lang version). The load-bearing, observable contract we assert:
// Keep returns a non-nil error AND fails open — the offending single include
// rule is indeterminate, so the record is KEPT (keep=true) rather than silently
// dropped, while the error is surfaced for OnFilterError.
func TestUltraFilt7_RuntimeNonBoolIncludeFailsOpen(t *testing.T) {
	// "score" is undefined at compile time (static type unknown) so New()
	// succeeds; at run time we bind it to a non-bool (an int).
	f, err := New([]string{"score"}, nil)
	if err != nil {
		t.Fatalf("expected successful compile for undefined-variable expr, got: %v", err)
	}

	keep, ferr := f.Keep(map[string]any{"score": 7})
	if ferr == nil {
		t.Fatalf("expected a runtime error from Keep on a non-bool result")
	}
	// expr-lang v1.17.8 surfaces the AsBool() coercion failure as a bool(int)
	// runtime error. Assert it identifies a bool-vs-int operation problem.
	if !strings.Contains(ferr.Error(), "bool") || !strings.Contains(ferr.Error(), "int") {
		t.Errorf("expected a bool/int coercion error, got: %v", ferr)
	}
	// Single include rule errored -> indeterminate -> fail open -> record kept.
	if !keep {
		t.Errorf("non-bool include eval error must fail open (keep=true), got keep=false")
	}
}

// TestUltraFilt7_RuntimeNonBoolExcludeKept verifies the conservative handling on
// the exclude side: a runtime non-bool exclude expression is treated as
// not-matched (exclude miss), so the record is KEPT, but the error is still
// surfaced via firstErr.
func TestUltraFilt7_RuntimeNonBoolExcludeKept(t *testing.T) {
	f, err := New(nil, []string{"score"})
	if err != nil {
		t.Fatalf("expected successful compile, got: %v", err)
	}

	keep, ferr := f.Keep(map[string]any{"score": 7})
	if ferr == nil {
		t.Fatalf("expected a runtime error from Keep on a non-bool result")
	}
	if !strings.Contains(ferr.Error(), "bool") || !strings.Contains(ferr.Error(), "int") {
		t.Errorf("expected a bool/int coercion error, got: %v", ferr)
	}
	// Exclude miss (treated as not-matched) => record kept.
	if !keep {
		t.Errorf("non-bool exclude result must be treated as not-matched (keep=true), got keep=false")
	}
}
