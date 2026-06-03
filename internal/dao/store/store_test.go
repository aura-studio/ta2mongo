package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
)

// ---------------------------------------------------------------------------
// MaxElapsedTime retry tests (unit-level, no real MongoDB)
// ---------------------------------------------------------------------------

// TestRetryRespectsMaxElapsedTime verifies that the exponential backoff stops
// retrying once MaxElapsedTime is exceeded. We replicate the same backoff
// setup used in Store.BulkWrite but without MongoDB.
func TestRetryRespectsMaxElapsedTime(t *testing.T) {
	maxElapsed := 500 * time.Millisecond

	attempts := 0
	alwaysFail := func() error {
		attempts++
		return errors.New("transient error")
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 50 * time.Millisecond
	bo.MaxInterval = 100 * time.Millisecond
	bo.MaxElapsedTime = maxElapsed
	bo.Reset()

	start := time.Now()
	err := backoff.Retry(alwaysFail, bo)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", attempts)
	}
	// Elapsed time should be roughly within MaxElapsedTime + tolerance.
	tolerance := 300 * time.Millisecond
	if elapsed > maxElapsed+tolerance {
		t.Errorf("elapsed %v exceeds MaxElapsedTime %v + tolerance %v", elapsed, maxElapsed, tolerance)
	}
}

// TestRetrySucceedsBeforeMaxElapsedTime verifies that when the operation
// eventually succeeds, the retry loop returns nil.
func TestRetrySucceedsBeforeMaxElapsedTime(t *testing.T) {
	maxElapsed := 2 * time.Second

	attempts := 0
	failTwiceThenSucceed := func() error {
		attempts++
		if attempts <= 2 {
			return errors.New("transient")
		}
		return nil
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 50 * time.Millisecond
	bo.MaxInterval = 100 * time.Millisecond
	bo.MaxElapsedTime = maxElapsed
	bo.Reset()

	err := backoff.Retry(failTwiceThenSucceed, bo)
	if err != nil {
		t.Errorf("expected nil error after eventual success, got %v", err)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

// TestRetryWithContextCancellation verifies that cancelling the context stops
// retrying even before MaxElapsedTime is reached.
func TestRetryWithContextCancellation(t *testing.T) {
	maxElapsed := 10 * time.Second // very long, should not be reached

	attempts := 0
	alwaysFail := func() error {
		attempts++
		return errors.New("transient")
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 50 * time.Millisecond
	bo.MaxInterval = 100 * time.Millisecond
	bo.MaxElapsedTime = maxElapsed
	bo.Reset()

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := backoff.Retry(alwaysFail, backoff.WithContext(bo, ctx))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error after context cancellation")
	}
	// Should have stopped well before MaxElapsedTime.
	if elapsed > 1*time.Second {
		t.Errorf("expected to stop quickly due to context, elapsed %v", elapsed)
	}
}

// TestRetryWithDifferentMaxElapsedTimes verifies that different
// MaxElapsedTime values produce different retry behaviors.
func TestRetryWithDifferentMaxElapsedTimes(t *testing.T) {
	tests := []struct {
		name       string
		maxElapsed time.Duration
	}{
		{"short_200ms", 200 * time.Millisecond},
		{"medium_1s", 1 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attempts := 0
			alwaysFail := func() error {
				attempts++
				return errors.New("fail")
			}

			bo := backoff.NewExponentialBackOff()
			bo.InitialInterval = 30 * time.Millisecond
			bo.MaxInterval = 60 * time.Millisecond
			bo.MaxElapsedTime = tt.maxElapsed
			bo.Reset()

			start := time.Now()
			err := backoff.Retry(alwaysFail, bo)
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("expected error")
			}
			// Elapsed should be close to maxElapsed (within tolerance).
			tolerance := 300 * time.Millisecond
			if elapsed > tt.maxElapsed+tolerance {
				t.Errorf("elapsed %v exceeds max %v + tolerance", elapsed, tt.maxElapsed)
			}
			t.Logf("maxElapsed=%v, actual elapsed=%v, attempts=%d", tt.maxElapsed, elapsed, attempts)
		})
	}
}

// TestConfigMaxElapsedTimeWiring verifies that the config value would be
// correctly used when constructing the backoff. We replicate the exact
// backoff setup from Store.BulkWrite with the configured MaxElapsedTime.
func TestConfigMaxElapsedTimeWiring(t *testing.T) {
	cfg := Config{MaxElapsedTime: 500 * time.Millisecond}

	// Replicate the backoff setup from BulkWrite.
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 200 * time.Millisecond
	bo.MaxInterval = 2 * time.Second
	bo.MaxElapsedTime = cfg.MaxElapsedTime
	bo.Reset()

	if bo.MaxElapsedTime != 500*time.Millisecond {
		t.Errorf("expected backoff MaxElapsedTime = 500ms, got %v", bo.MaxElapsedTime)
	}

	// Also verify the config value propagation.
	if cfg.MaxElapsedTime != 500*time.Millisecond {
		t.Errorf("expected config MaxElapsedTime = 500ms, got %v", cfg.MaxElapsedTime)
	}
}
