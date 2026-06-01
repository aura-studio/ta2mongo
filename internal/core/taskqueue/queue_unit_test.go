package taskqueue

import (
	"testing"
	"time"
)

// TestBackoff_Exponential verifies the retry backoff doubles per attempt and
// is capped, and that a zero base disables it (used by claim-semantics tests).
func TestBackoff_Exponential(t *testing.T) {
	q := NewQueue(nil).WithRetryBackoff(2*time.Second, time.Minute)
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 0},
		{1, 2 * time.Second},
		{2, 4 * time.Second},
		{3, 8 * time.Second},
		{4, 16 * time.Second},
		{5, 32 * time.Second},
		{6, time.Minute}, // 64s capped to 60s
		{10, time.Minute},
	}
	for _, c := range cases {
		if got := q.backoff(c.attempts); got != c.want {
			t.Errorf("backoff(%d) = %v, want %v", c.attempts, got, c.want)
		}
	}

	if got := NewQueue(nil).WithRetryBackoff(0, 0).backoff(3); got != 0 {
		t.Errorf("disabled backoff = %v, want 0", got)
	}
}
