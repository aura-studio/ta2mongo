package single

import (
	"sync"
	"testing"
)

// TestCountersSnapshot verifies the counter increments map to the right
// snapshot fields.
func TestCountersSnapshot(t *testing.T) {
	var c Counters
	c.OnLine()
	c.OnParseOK()
	c.OnParseError()
	c.OnIdentityError()
	c.OnUserWrite()
	c.OnEventWrite()
	c.OnDeadLetter()
	c.OnWriteError()
	c.OnFiltered()
	c.OnFilterError()

	got := c.Snapshot()
	want := Snapshot{
		TotalLines: 1, ParsedOK: 1, ParseErrors: 1, IdentityErrors: 1,
		UserWrites: 1, EventWrites: 1, DeadLetters: 1, WriteErrors: 1,
		Filtered: 1, FilterErrors: 1,
	}
	if got != want {
		t.Errorf("snapshot = %+v, want %+v", got, want)
	}
}

func TestCountersConcurrent(t *testing.T) {
	var c Counters
	const goroutines, perG = 8, 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				c.OnLine()
				c.OnEventWrite()
			}
		}()
	}
	wg.Wait()
	snap := c.Snapshot()
	if snap.TotalLines != goroutines*perG || snap.EventWrites != goroutines*perG {
		t.Errorf("concurrent counts = lines:%d events:%d, want %d each",
			snap.TotalLines, snap.EventWrites, goroutines*perG)
	}
}
