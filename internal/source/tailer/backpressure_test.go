package tailer

// Backpressure regression tests for the v1.5 release gate (doc/test.md group F).
//
// When the downstream consumer stalls (in production: MongoDB writes throttled
// or paused), the tailer's out channel (cap 2000) fills and each per-file tail
// goroutine parks on `out <- line`. These tests assert that this backpressure
//   - does not deadlock, panic, or grow memory without bound (F1), and
//   - does not pin a deleted fd: a send-blocked goroutine still releases its fd
//     when its file is reaped, because every `out <- line` select also waits on
//     ctx.Done() (F2).
//
// Helpers (drain, appendLines, mkLines, waitTailedCount, waitDeletedFDs,
// requireLinux, allModes) are shared with lifecycle_test.go.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// F1: with the downstream fully stalled, writing far more than the 2000-line
// buffer must not kill or wedge the tail goroutine; once draining resumes every
// line is delivered (at-least-once — duplicates tolerated, loss is not).
func TestBackpressure_F1_NoDeadlockNoLossWhenDownstreamStalls(t *testing.T) {
	for _, mode := range allModes {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			logFile := filepath.Join(dir, "app.log")
			if err := os.WriteFile(logFile, nil, 0644); err != nil {
				t.Fatal(err)
			}

			tl := New([]string{dir + "/*.log"}, 100*time.Millisecond, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := tl.Run(ctx) // intentionally not drained yet — full backpressure

			if got := waitTailedCount(tl, 1, 2*time.Second); got != 1 {
				t.Fatalf("%s: tailer not attached, tailed=%d", mode, got)
			}

			// Phase 1: downstream stalled. Write 3x the channel capacity so the
			// tail goroutine must block on send rather than buffer everything.
			const n = 6000
			lines := mkLines("bp", n)
			appendLines(t, logFile, lines)

			// The goroutine must stay alive and parked (no panic/deadlock/spin):
			// memory stays bounded because a blocked send reads no further. We
			// observe liveness via the tailed map staying at exactly 1.
			time.Sleep(time.Second)
			if got := tl.TailedCount(); got != 1 {
				t.Fatalf("%s: tail goroutine vanished under backpressure (panic/deadlock?), tailed=%d", mode, got)
			}

			// Phase 2: relieve backpressure. Every line must still arrive,
			// proving the blocked send resumed cleanly.
			sink := drain(out)
			sink.waitForLine(t, lines[n-1], 25*time.Second)
			for _, l := range lines {
				if !sink.has(l) {
					t.Fatalf("%s: line lost after backpressure relief: %q", mode, l)
				}
			}
		})
	}
}

// F2: a tail goroutine blocked on `out <- line` (downstream stalled) must still
// release its fd within a few rescans when the file is deleted — the reap path
// cancels the goroutine's context, and the send select unblocks on ctx.Done().
// This is the exact path that would pin a deleted-but-open fd if the send had no
// cancellation arm.
func TestBackpressure_F2_FDReleasedWhileBlockedOnSend(t *testing.T) {
	requireLinux(t)
	for _, mode := range allModes {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			logFile := filepath.Join(dir, "app.log")
			if err := os.WriteFile(logFile, []byte("seed\n"), 0644); err != nil {
				t.Fatal(err)
			}

			rescan := 150 * time.Millisecond
			tl := New([]string{dir + "/*.log"}, rescan, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			_ = tl.Run(ctx) // never drained: out fills, the send blocks

			if got := waitTailedCount(tl, 1, 2*time.Second); got != 1 {
				t.Fatalf("%s: tailer not attached, tailed=%d", mode, got)
			}

			// Saturate the channel so the goroutine parks on send.
			appendLines(t, logFile, mkLines("bp", 6000))
			time.Sleep(500 * time.Millisecond)

			// Delete the file while the goroutine is blocked on send.
			if err := os.Remove(logFile); err != nil {
				t.Fatalf("remove: %v", err)
			}

			bound := 6*rescan + 4*time.Second
			if got := waitDeletedFDs(t, dir, 0, bound); got != 0 {
				t.Fatalf("%s: deleted fd not released while blocked on send within %s; deleted=%d (fd leak under backpressure)", mode, bound, got)
			}
			if got := waitTailedCount(tl, 0, 2*time.Second); got != 0 {
				t.Fatalf("%s: tail goroutine not reaped while blocked on send; tailed=%d", mode, got)
			}
		})
	}
}
