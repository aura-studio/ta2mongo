package tailer

// Ultra test for TAIL-21 (doc/ultra_test.md §4): tailFile's error-retry loop
// must be paced by pollInterval — one warn per failed attempt followed by a
// sleep — never a busy-spin that floods the log and burns CPU.
//
// Production contract under test (tailer.go, tailFile):
//
//	for {
//		if err := t.readFollowFile(ctx, path, out); err != nil {
//			if ctx.Err() != nil { return }
//			logging.WithError(err).WithField("path", path).
//				Warn("tailer: error reading file, will retry")
//		}
//		select {
//		case <-ctx.Done(): return
//		case <-time.After(t.pollInterval):
//		}
//	}
//
// Linux-only scenario: poll mode holds the file via plain os.Open (no
// FILE_SHARE_DELETE), so on Windows the OS refuses to delete a tailed file
// and the failure mode cannot even be set up.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/aura-studio/tango/internal/logging"
)

// retryWarnHook is a logrus hook that counts the exact per-attempt retry warn
// emitted by Tailer.tailFile for one specific path, capturing the attached
// error so the test can assert it is the genuine ENOENT from os.Open/os.Stat.
type retryWarnHook struct {
	path string

	mu      sync.Mutex
	count   int
	lastErr error
}

func (h *retryWarnHook) Levels() []logrus.Level { return []logrus.Level{logrus.WarnLevel} }

func (h *retryWarnHook) Fire(e *logrus.Entry) error {
	if e.Message != "tailer: error reading file, will retry" {
		return nil
	}
	if p, _ := e.Data["path"].(string); p != h.path {
		return nil
	}
	err, _ := e.Data[logrus.ErrorKey].(error)
	h.mu.Lock()
	h.count++
	h.lastErr = err
	h.mu.Unlock()
	return nil
}

func (h *retryWarnHook) snapshot() (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count, h.lastErr
}

func TestUltraRetry_TAIL21_ErrorRetryPacedByPollInterval(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("poll mode opens with plain os.Open (no FILE_SHARE_DELETE); deleting a tailed file is a Linux-only scenario")
	}

	dir := t.TempDir()
	logFile := filepath.Join(dir, "app.log")
	if err := os.WriteFile(logFile, []byte("line1\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Count the exact retry warn for this file on the shared logger. Swap in a
	// clean hook set and restore the previous one via t.Cleanup.
	hook := &retryWarnHook{path: logFile}
	prevHooks := logging.L().ReplaceHooks(make(logrus.LevelHooks))
	logging.L().AddHook(hook)
	t.Cleanup(func() { logging.L().ReplaceHooks(prevHooks) })

	const pollEvery = 50 * time.Millisecond
	// LONG rescan (30s): reapMissing never fires during this test, so the only
	// thing driving the goroutine after deletion is tailFile's own retry loop.
	tl := New([]string{dir + "/*.log"}, 30*time.Second, TailModePoll).WithTuning(pollEvery, 0)
	if tl.pollInterval != pollEvery {
		t.Fatalf("WithTuning: pollInterval = %v, want %v", tl.pollInterval, pollEvery)
	}
	if tl.maxLineSize != defaultMaxLineSize {
		t.Fatalf("WithTuning(.., 0) must keep the default maxLineSize %d, got %d",
			defaultMaxLineSize, tl.maxLineSize)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := tl.Run(ctx)

	// The pre-existing content must arrive (tailFile reads from the start),
	// proving the file is attached before we delete it.
	select {
	case line := <-out:
		if line != "line1" {
			t.Fatalf("first tailed line = %q, want %q", line, "line1")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the initial line from the tailed file")
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for range out {
		}
	}()

	if got := waitTailedCount(tl, 1, 2*time.Second); got != 1 {
		t.Fatalf("expected 1 tailed file before delete, got %d", got)
	}

	// Delete the file out from under the poll loop. Every subsequent attempt
	// (the os.Stat inside readFollowFile, then os.Open on each retry) fails
	// with ENOENT, so tailFile warns once per cycle and sleeps pollInterval.
	if err := os.Remove(logFile); err != nil {
		t.Fatal(err)
	}

	// Wait for the first per-attempt warn (deletion is noticed within ~1 poll).
	firstBy := time.Now().Add(2 * time.Second)
	for {
		if n, _ := hook.snapshot(); n > 0 {
			break
		}
		if time.Now().After(firstBy) {
			t.Fatalf("tailFile never logged %q after the file was deleted",
				"tailer: error reading file, will retry")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// TAIL-21 core: measure the warn rate over a fixed 600ms window. A paced
	// loop (warn → sleep pollInterval → retry) yields ~600/50 = 12 warns; a
	// busy-spin yields hundreds-to-thousands; a stalled loop yields 0.
	base, _ := hook.snapshot()
	const window = 600 * time.Millisecond
	time.Sleep(window)
	cur, lastErr := hook.snapshot()
	rate := cur - base
	if rate < 3 || rate > 30 {
		t.Fatalf("retry warn fired %d times in %v with pollInterval=%v; want 3..30 (~12): 0-2 = stalled, hundreds = busy-spin without the pollInterval wait",
			rate, window, pollEvery)
	}

	// Each failed attempt must carry the genuine ENOENT via WithError.
	if lastErr == nil || !errors.Is(lastErr, fs.ErrNotExist) {
		t.Fatalf("retry warn error = %v, want fs.ErrNotExist (the real os.Open/os.Stat failure)", lastErr)
	}

	// With the 30s rescan, reapMissing has NOT fired: the per-file goroutine
	// must still be alive, parked in its paced retry loop — not exited.
	if got := tl.TailedCount(); got != 1 {
		t.Fatalf("TailedCount = %d during retry, want 1 (reapMissing must not have fired under a 30s rescan)", got)
	}

	// The pollInterval wait must also honour ctx.Done: cancelling mid-retry
	// must release the goroutine promptly and close the output channel.
	cancel()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		t.Fatal("output channel did not close within 2s of cancel — the retry loop is not honouring ctx.Done")
	}
	if got := waitTailedCount(tl, 0, 2*time.Second); got != 0 {
		t.Fatalf("TailedCount = %d after cancel, want 0", got)
	}

	// After shutdown the warn stream must stop: tailFile checks ctx.Err()
	// before warning, so no retry can fire once every goroutine has exited.
	quiesced, _ := hook.snapshot()
	time.Sleep(4 * pollEvery)
	final, _ := hook.snapshot()
	if final != quiesced {
		t.Fatalf("retry warns kept firing after shutdown: %d -> %d", quiesced, final)
	}
}
