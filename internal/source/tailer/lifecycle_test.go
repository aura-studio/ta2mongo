package tailer

// File-lifecycle and fd/goroutine-leak regression tests for the v1.5 release
// gate (doc/test.md groups C and D).
//
// IRON RULE: the deleted-but-open / /proc/<pid>/fd / inode-reuse assertions are
// Linux semantics. On non-Linux they are skipped, never "passed" — running them
// on the Windows host would be a false green. Run via:
//   docker compose -f test/docker-compose.ubuntu.yml run --rm tango-test \
//     go test -race -run 'Lifecycle|FD|Goroutine' ./internal/source/tailer/...

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// allModes is the set of tail strategies every lifecycle invariant must hold
// for. The fd leak this gate guards against was mode-specific (hybrid/event
// followed the deleted inode forever), so each case runs against all three.
var allModes = []string{TailModePoll, TailModeHybrid, TailModeEvent}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// lineSink concurrently collects every line emitted by a tailer's output
// channel. Tailing is at-least-once across rotations, so we record a multiset
// (count per line) and expose membership; callers assert coverage, not exact
// equality, where duplicates are acceptable (see doc/test.md H3).
type lineSink struct {
	mu    sync.Mutex
	count map[string]int
	total int
}

func drain(out <-chan string) *lineSink {
	s := &lineSink{count: make(map[string]int)}
	go func() {
		for line := range out {
			s.mu.Lock()
			s.count[line]++
			s.total++
			s.mu.Unlock()
		}
	}()
	return s
}

func (s *lineSink) has(line string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count[line] > 0
}

// waitForLine polls until the sink has seen line, or fails after timeout.
func (s *lineSink) waitForLine(t *testing.T, line string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.has(line) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for line %q", timeout, line)
}

// countDeletedFDs reads /proc/self/fd and counts descriptors whose target ends
// in " (deleted)" — files unlinked from the filesystem but still held open by
// this process. That is the exact signature of the v1.5 leak (deleted-but-open
// log files accumulating until the volume fills). When dir != "" only deleted
// fds whose path is under dir are counted, isolating this test's files from any
// unrelated deleted fds elsewhere in the test process. Linux-only.
func countDeletedFDs(t *testing.T, dir string) int {
	t.Helper()
	const fdDir = "/proc/self/fd"
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		t.Fatalf("read %s: %v", fdDir, err)
	}
	n := 0
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			continue // fd closed between ReadDir and Readlink; ignore
		}
		if !strings.HasSuffix(target, " (deleted)") {
			continue
		}
		if dir != "" {
			path := strings.TrimSuffix(target, " (deleted)")
			if !strings.HasPrefix(path, dir) {
				continue
			}
		}
		n++
	}
	return n
}

// requireLinux skips deleted-fd / proc-based assertions off Linux rather than
// letting them spuriously pass.
func requireLinux(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("deleted-but-open fd accounting is a Linux (/proc/self/fd) semantic; refusing to false-green off Linux")
	}
}

// waitDeletedFDs polls countDeletedFDs(dir) until it reaches want or timeout,
// returning the last observed value.
func waitDeletedFDs(t *testing.T, dir string, want int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var n int
	for {
		n = countDeletedFDs(t, dir)
		if n == want || time.Now().After(deadline) {
			return n
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func appendLines(t *testing.T, path string, lines []string) {
	t.Helper()
	fd, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		t.Fatalf("open %s for append: %v", path, err)
	}
	defer fd.Close()
	for _, l := range lines {
		if _, err := fd.WriteString(l + "\n"); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

func mkLines(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%04d", prefix, i)
	}
	return out
}

// ---------------------------------------------------------------------------
// C. tailer file lifecycle — poll / event / hybrid each
// ---------------------------------------------------------------------------

// C1: continuous append — every written line is emitted, none lost. Writes are
// paced in small bursts so event mode (which can stall under sustained
// concurrent writes, see config.go) is exercised fairly.
func TestLifecycle_C1_ContinuousAppend(t *testing.T) {
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
			sink := drain(tl.Run(ctx))

			if got := waitTailedCount(tl, 1, 2*time.Second); got != 1 {
				t.Fatalf("expected tailer attached before writing, tailed=%d", got)
			}

			const n = 1500
			lines := mkLines("app", n)
			fd, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
			if err != nil {
				t.Fatal(err)
			}
			for i, l := range lines {
				_, _ = fd.WriteString(l + "\n")
				if i%50 == 49 {
					time.Sleep(2 * time.Millisecond) // let the watcher drain
				}
			}
			fd.Close()

			// Last line proves no tail-end loss; spot-check the first too.
			sink.waitForLine(t, lines[n-1], 15*time.Second)
			sink.waitForLine(t, lines[0], 2*time.Second)

			// No line should be missing in between.
			for _, l := range lines {
				if !sink.has(l) {
					t.Fatalf("%s: missing line %q (dropped)", mode, l)
				}
			}
		})
	}
}

// C2: rotate (rename current away + create a new file with the same name). The
// residue of the renamed file must still be read, and the new file must be
// tailed from its start. Duplicates are tolerated (at-least-once); loss is not.
func TestLifecycle_C2_Rotate(t *testing.T) {
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
			sink := drain(tl.Run(ctx))
			if got := waitTailedCount(tl, 1, 2*time.Second); got != 1 {
				t.Fatalf("tailer not attached, tailed=%d", got)
			}

			before := mkLines("before", 200)
			appendLines(t, logFile, before)
			sink.waitForLine(t, before[len(before)-1], 5*time.Second)

			// Rotate: rename current to a backup, create a fresh current.
			if err := os.Rename(logFile, filepath.Join(dir, "app.log.1")); err != nil {
				t.Fatalf("rotate rename: %v", err)
			}
			if err := os.WriteFile(logFile, nil, 0644); err != nil {
				t.Fatalf("recreate after rotate: %v", err)
			}
			after := mkLines("after", 200)
			appendLines(t, logFile, after)

			sink.waitForLine(t, after[len(after)-1], 8*time.Second)

			// Every line from both the pre-rotate and post-rotate file present.
			for _, l := range append(append([]string{}, before...), after...) {
				if !sink.has(l) {
					t.Fatalf("%s: line lost across rotation: %q", mode, l)
				}
			}
		})
	}
}

// C3: truncate (file shrinks in place). The tailer must seek back to the start
// and re-read; it must not hang or panic.
func TestLifecycle_C3_Truncate(t *testing.T) {
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
			sink := drain(tl.Run(ctx))
			if got := waitTailedCount(tl, 1, 2*time.Second); got != 1 {
				t.Fatalf("tailer not attached, tailed=%d", got)
			}

			first := mkLines("pre-truncate", 100)
			appendLines(t, logFile, first)
			sink.waitForLine(t, first[len(first)-1], 5*time.Second)

			// Shrink the file to zero in place.
			if err := os.Truncate(logFile, 0); err != nil {
				t.Fatalf("truncate: %v", err)
			}
			time.Sleep(200 * time.Millisecond)

			second := mkLines("post-truncate", 100)
			appendLines(t, logFile, second)

			// Post-truncate content must arrive — proves no deadlock/panic and
			// that the reader recovered from size < offset.
			sink.waitForLine(t, second[len(second)-1], 8*time.Second)
			for _, l := range second {
				if !sink.has(l) {
					t.Fatalf("%s: missing post-truncate line %q", mode, l)
				}
			}
		})
	}
}

// C4: delete does not recur — the goroutine for a deleted file exits within
// ~2x rescanInterval. (The fd-release consequence is asserted in the D group;
// here we pin the timing bound the spec calls out.)
func TestLifecycle_C4_DeleteReaped(t *testing.T) {
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
			drain(tl.Run(ctx))

			if got := waitTailedCount(tl, 1, 2*time.Second); got != 1 {
				t.Fatalf("tailer not attached, tailed=%d", got)
			}
			if err := os.Remove(logFile); err != nil {
				t.Fatalf("remove: %v", err)
			}

			// Allow a couple of reap cycles plus scheduling slack.
			bound := 4*rescan + 2*time.Second
			start := time.Now()
			if got := waitTailedCount(tl, 0, bound); got != 0 {
				t.Fatalf("%s: goroutine not reaped within %s; tailed=%d", mode, bound, got)
			}
			t.Logf("%s: reaped in %s (bound %s)", mode, time.Since(start), bound)
		})
	}
}

// ---------------------------------------------------------------------------
// D. fd / goroutine leak regression  ★ core gate ★
// ---------------------------------------------------------------------------

// D1: validate the countDeletedFDs helper itself — open a file, unlink it while
// open (Linux deleted-but-open), confirm the count rises, then close and
// confirm it falls back.
func TestFD_D1_CountDeletedHelper(t *testing.T) {
	requireLinux(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "victim.log")
	if err := os.WriteFile(p, []byte("x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	base := countDeletedFDs(t, dir)

	fd, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if got := countDeletedFDs(t, dir); got != base+1 {
		t.Fatalf("expected deleted-fd count %d after unlinking an open file, got %d", base+1, got)
	}
	fd.Close()
	if got := countDeletedFDs(t, dir); got != base {
		t.Fatalf("expected deleted-fd count back to %d after close, got %d", base, got)
	}
}

// D2: single file — tail → delete → after ~2x rescan the deleted-fd count for
// that file is back to 0 (the goroutine stopped following the deleted inode and
// released its fd). Runs for every mode (covers D2 for hybrid and D5 for
// event/poll).
func TestFD_D2_SingleFileReleasesFD(t *testing.T) {
	requireLinux(t)
	for _, mode := range allModes {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			logFile := filepath.Join(dir, "app.log")
			if err := os.WriteFile(logFile, []byte("line1\n"), 0644); err != nil {
				t.Fatal(err)
			}

			rescan := 150 * time.Millisecond
			tl := New([]string{dir + "/*.log"}, rescan, mode)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			drain(tl.Run(ctx))

			if got := waitTailedCount(tl, 1, 2*time.Second); got != 1 {
				t.Fatalf("tailer not attached, tailed=%d", got)
			}
			// Sanity: the open log shows up as a live (non-deleted) fd now.
			if err := os.Remove(logFile); err != nil {
				t.Fatalf("remove: %v", err)
			}

			bound := 4*rescan + 3*time.Second
			if got := waitDeletedFDs(t, dir, 0, bound); got != 0 {
				t.Fatalf("%s: deleted fd not released within %s; deleted=%d (fd leak)", mode, bound, got)
			}
			if got := waitTailedCount(tl, 0, time.Second); got != 0 {
				t.Fatalf("%s: tailed map not drained; got %d", mode, got)
			}
		})
	}
}

// rotateStress creates/fills/deletes log files keeping a sliding window of
// `window` live files for `iters` iterations, sampling the peak deleted-fd
// count under dir. It returns the peak observed during the run.
func rotateStress(t *testing.T, mode string, iters, window int) (peak int, dir string, tl *Tailer, stop func()) {
	t.Helper()
	dir = t.TempDir()
	rescan := 50 * time.Millisecond
	tl = New([]string{dir + "/*.log"}, rescan, mode)
	ctx, cancel := context.WithCancel(context.Background())
	drain(tl.Run(ctx))

	for i := 0; i < iters; i++ {
		cur := filepath.Join(dir, fmt.Sprintf("ta.test-%05d.log", i))
		appendLines(t, cur, mkLines("r", 20))
		if old := i - window; old >= 0 {
			_ = os.Remove(filepath.Join(dir, fmt.Sprintf("ta.test-%05d.log", old)))
		}
		if d := countDeletedFDs(t, dir); d > peak {
			peak = d
		}
		// Pace so the reaper (rescan) keeps up; without this we'd only be
		// measuring create-vs-reap latency, not steady state.
		time.Sleep(5 * time.Millisecond)
	}
	return peak, dir, tl, cancel
}

// D3: 100 rotations, sliding window — steady-state deleted-fd count stays
// bounded (does not grow with iteration count), and after activity stops every
// deleted fd is reaped back to 0. Runs all modes (D3 + D5).
func TestFD_D3_RotationSteadyState(t *testing.T) {
	requireLinux(t)
	const (
		iters  = 100
		window = 5
	)
	for _, mode := range allModes {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			peak, dir, _, stop := rotateStress(t, mode, iters, window)
			defer stop()

			// Peak must be bounded by the live-file window plus reap-latency
			// slack — crucially independent of `iters`. A leak would make peak
			// scale with iteration count.
			if peak > window+15 {
				t.Fatalf("%s: deleted-fd peak %d exceeds bound %d over %d rotations (leak: grows with iterations)",
					mode, peak, window+15, iters)
			}
			// After rotation stops, the remaining files get deleted by the
			// window logic on the last iters; everything must reap to 0.
			for i := iters - window; i < iters; i++ {
				_ = os.Remove(filepath.Join(dir, fmt.Sprintf("ta.test-%05d.log", i)))
			}
			if got := waitDeletedFDs(t, dir, 0, 5*time.Second); got != 0 {
				t.Fatalf("%s: %d deleted fds still held after quiescing (fd leak)", mode, got)
			}
			t.Logf("%s: %d rotations, deleted-fd peak=%d, quiesced to 0", mode, iters, peak)
		})
	}
}

// D4: goroutine and tailed-map stability — after 1000 rotations and a quiesce,
// NumGoroutine returns to ~baseline and the tailed map is small (bounded by the
// live-file window), i.e. neither grows monotonically with rotation count.
func TestGoroutine_D4_NoLeakOverManyRotations(t *testing.T) {
	requireLinux(t)
	const (
		iters  = 1000
		window = 5
	)
	// Baseline after the tailer is up but before churn.
	dir := t.TempDir()
	rescan := 50 * time.Millisecond
	tl := New([]string{dir + "/*.log"}, rescan, TailModeHybrid)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	drain(tl.Run(ctx))
	time.Sleep(200 * time.Millisecond)
	baseGo := runtime.NumGoroutine()

	for i := 0; i < iters; i++ {
		cur := filepath.Join(dir, fmt.Sprintf("ta.test-%05d.log", i))
		appendLines(t, cur, mkLines("r", 5))
		if old := i - window; old >= 0 {
			_ = os.Remove(filepath.Join(dir, fmt.Sprintf("ta.test-%05d.log", old)))
		}
		if i%50 == 49 {
			time.Sleep(5 * time.Millisecond)
		}
	}
	// Delete the final window so everything can be reaped.
	for i := iters - window; i < iters; i++ {
		_ = os.Remove(filepath.Join(dir, fmt.Sprintf("ta.test-%05d.log", i)))
	}

	// Let rescans reap the churn.
	if got := waitTailedCount(tl, 0, 8*time.Second); got != 0 {
		t.Fatalf("tailed map did not drain after %d rotations; got %d (leak)", iters, got)
	}

	// Allow goroutines to wind down, then compare to baseline.
	var nowGo int
	deadline := time.Now().Add(5 * time.Second)
	for {
		runtime.GC()
		nowGo = runtime.NumGoroutine()
		if nowGo <= baseGo+5 || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if nowGo > baseGo+5 {
		t.Fatalf("goroutines grew from %d to %d over %d rotations (leak)", baseGo, nowGo, iters)
	}
	t.Logf("D4: %d rotations, goroutines %d -> %d (baseline +%d)", iters, baseGo, nowGo, nowGo-baseGo)
}
