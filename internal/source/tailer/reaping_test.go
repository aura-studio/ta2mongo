package tailer

// v1.5.1 increment tests (doc/test2.md group C): the per-mode delete / inode
// self-check that runs INDEPENDENTLY of the coarse reapMissing rescan.
//
// The trick throughout: configure a LONG rescanInterval so reapMissing cannot be
// the thing that releases the fd within the test window. Anything that happens
// fast is therefore the per-file self-check — the event/hybrid stat ticker
// (hybridPollInterval) or poll's own os.Stat loop — which is exactly the v1.5.1
// behaviour under test (before v1.5.1 the event mode had no ticker and pinned the
// deleted inode until the next rescan, or forever).
//
// Linux-only assertions are gated with requireLinux. Shared helpers (allModes,
// drain, appendLines, mkLines, countDeletedFDs, waitDeletedFDs, waitTailedCount,
// requireLinux, rotateStress) come from lifecycle_test.go / tailer_test.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// longRescan is far larger than any per-mode self-check interval, so a fd that is
// released quickly proves the self-check (not reapMissing) did it.
const longRescan = 30 * time.Second

// C2 (event) + C1 (hybrid): after a delete, the stat ticker releases the fd well
// before one rescan. For event mode the tail goroutine also exits (TailedCount→0)
// because tailFileEvent returns; hybrid's goroutine loops to re-open the path, so
// only the fd-release (the leak-relevant part) is asserted fast there.
func TestReap_C2_TickerReleasesFDBeforeRescan(t *testing.T) {
	requireLinux(t)
	cases := []struct {
		mode       string
		expectExit bool // does the tail goroutine exit on delete (vs. loop to reopen)?
	}{
		{TailModeEvent, true},
		{TailModeHybrid, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.mode, func(t *testing.T) {
			dir := t.TempDir()
			logFile := filepath.Join(dir, "app.log")
			if err := os.WriteFile(logFile, []byte("seed\n"), 0644); err != nil {
				t.Fatal(err)
			}

			tl := New([]string{dir + "/*.log"}, longRescan, tc.mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			drain(tl.Run(ctx))
			if got := waitTailedCount(tl, 1, 3*time.Second); got != 1 {
				t.Fatalf("%s: tailer not attached, tailed=%d", tc.mode, got)
			}

			if err := os.Remove(logFile); err != nil {
				t.Fatalf("remove: %v", err)
			}

			// The ticker fires every hybridPollInterval (500ms); 3s is comfortably
			// under the 30s rescan, so a pass here is the ticker, not reapMissing.
			bound := 3 * time.Second
			if got := waitDeletedFDs(t, dir, 0, bound); got != 0 {
				t.Fatalf("%s: deleted fd not released by stat ticker within %s (rescan=%s); deleted=%d — event ticker regression",
					tc.mode, bound, longRescan, got)
			}
			if tc.expectExit {
				if got := waitTailedCount(tl, 0, bound); got != 0 {
					t.Fatalf("%s: tail goroutine did not exit on delete via ticker; tailed=%d", tc.mode, got)
				}
			}
		})
	}
}

// C4: in-place rotation — rename the current file away and recreate a NEW inode
// at the same path (no delete of the watched path). The stat ticker's
// os.SameFile check must notice the inode swap and reopen the new inode from the
// start, so lines written to the new file are tailed well before one rescan.
func TestReap_C4_InPlaceInodeRotationReopens(t *testing.T) {
	for _, mode := range []string{TailModeHybrid, TailModeEvent} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			logFile := filepath.Join(dir, "app.log")
			if err := os.WriteFile(logFile, nil, 0644); err != nil {
				t.Fatal(err)
			}

			tl := New([]string{dir + "/*.log"}, longRescan, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sink := drain(tl.Run(ctx))
			if got := waitTailedCount(tl, 1, 3*time.Second); got != 1 {
				t.Fatalf("%s: tailer not attached, tailed=%d", mode, got)
			}

			before := mkLines("before", 50)
			appendLines(t, logFile, before)
			sink.waitForLine(t, before[len(before)-1], 5*time.Second)

			// In-place rotation: same path, new inode.
			if err := os.Rename(logFile, filepath.Join(dir, "app.log.1")); err != nil {
				t.Fatalf("rotate rename: %v", err)
			}
			if err := os.WriteFile(logFile, nil, 0644); err != nil {
				t.Fatalf("recreate (new inode): %v", err)
			}
			after := mkLines("after", 50)
			appendLines(t, logFile, after)

			// Must arrive within ~ticker latency, well under the 30s rescan — so it
			// is the os.SameFile self-check reopening, not a rediscovery rescan.
			sink.waitForLine(t, after[len(after)-1], 6*time.Second)
			for _, l := range after {
				if !sink.has(l) {
					t.Fatalf("%s: post-inode-swap line lost: %q", mode, l)
				}
			}
		})
	}
}

// C5: 200 hybrid rotations — steady-state deleted-fd stays bounded by the live
// window (does not grow with rotation count), and everything reaps to 0 once the
// churn stops.
func TestReap_C5_200RotationsHybridSteady(t *testing.T) {
	requireLinux(t)
	const (
		iters  = 200
		window = 5
	)
	peak, dir, tl, stop := rotateStress(t, TailModeHybrid, iters, window)
	defer stop()

	if peak > window+15 {
		t.Fatalf("deleted-fd peak %d exceeds bound %d over %d rotations (leak grows with iterations)",
			peak, window+15, iters)
	}
	// Delete the final live window and confirm full quiesce.
	for i := iters - window; i < iters; i++ {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("ta.test-%05d.log", i)))
	}
	if got := waitDeletedFDs(t, dir, 0, 6*time.Second); got != 0 {
		t.Fatalf("%d deleted fds still held after quiescing %d rotations (leak)", got, iters)
	}
	if got := waitTailedCount(tl, 0, 6*time.Second); got != 0 {
		t.Fatalf("tailed map did not drain after %d rotations; got %d", iters, got)
	}
	t.Logf("C5: %d rotations, deleted-fd peak=%d, quiesced to 0", iters, peak)
}
