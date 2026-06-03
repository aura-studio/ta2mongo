package report

import (
	"sync"
	"testing"
)

// TestRunStatsSnapshot verifies the report service's counter increments and
// snapshot are wired correctly and are safe under concurrent updates.
func TestRunStatsSnapshot(t *testing.T) {
	var s runStats
	s.OnLine()
	s.OnParseOK()
	s.OnParseError()
	s.OnIdentityError()
	s.OnUserWrite()
	s.OnEventWrite()
	s.OnDeadLetter()
	s.OnWriteError()
	s.OnFiltered()
	s.OnFilterError()

	got := s.snapshot()
	want := runSnapshot{
		totalLines: 1, parsedOK: 1, parseErrors: 1, identityErrors: 1,
		userWrites: 1, eventWrites: 1, deadLetters: 1, writeErrors: 1,
		filtered: 1, filterErrors: 1,
	}
	if got != want {
		t.Errorf("snapshot = %+v, want %+v", got, want)
	}
}

func TestRunStatsConcurrent(t *testing.T) {
	var s runStats
	const goroutines, perG = 8, 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perG; j++ {
				s.OnLine()
				s.OnEventWrite()
			}
		}()
	}
	wg.Wait()
	snap := s.snapshot()
	if snap.totalLines != goroutines*perG || snap.eventWrites != goroutines*perG {
		t.Errorf("concurrent counts = lines:%d events:%d, want %d each",
			snap.totalLines, snap.eventWrites, goroutines*perG)
	}
}
