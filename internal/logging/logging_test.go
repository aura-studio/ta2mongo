package logging

import (
	"sync"
	"testing"
)

// TestRecover_SwallowsPanic verifies Recover catches a panic so it does not
// propagate (and thus cannot crash the process).
func TestRecover_SwallowsPanic(t *testing.T) {
	didNotEscape := func() (ok bool) {
		defer func() {
			if r := recover(); r != nil {
				ok = false // panic escaped Recover
			}
		}()
		func() {
			defer Recover("unit test")
			panic("boom")
		}()
		return true
	}()
	if !didNotEscape {
		t.Fatal("Recover let the panic escape")
	}
}

// TestRecover_InGoroutine verifies a panicking goroutine guarded by Recover does
// not take the process down; the goroutine simply ends.
func TestRecover_InGoroutine(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer Recover("unit test goroutine")
		panic("kaboom")
	}()
	wg.Wait() // would never return (or process would crash) if not recovered
}

// TestRecover_NoPanicIsNoop verifies Recover is a no-op when nothing panics.
func TestRecover_NoPanicIsNoop(t *testing.T) {
	func() {
		defer Recover("unit test noop")
	}()
}
