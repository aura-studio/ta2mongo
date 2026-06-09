// Command soak drives the v1.5 release-gate soak tests (doc/test.md groups E and
// G). It writes ThinkingData-shaped log lines through a *real* natefinch/lumberjack
// rotator (the same library production uses) and tails the same glob with tango's
// hybrid Tailer, sampling four curves every interval:
//
//	deleted-fd   — entries in /proc/self/fd whose target ends in " (deleted)"
//	goroutines   — runtime.NumGoroutine()
//	rss          — VmRSS from /proc/self/status
//	fs-used      — statfs(dir): used bytes of the backing filesystem
//
// The leak this gate guards against (a tail goroutine that keeps a rotated-away
// inode open) shows up as deleted-fd AND fs-used climbing monotonically with
// time. With the fix, lumberjack caps live files at (MaxBackups+1)*MaxSize and
// every reaped fd frees its blocks, so all four curves are flat after warmup.
//
// IRON RULE: deleted-but-open / /proc/self/fd are Linux semantics — this binary
// refuses to run anywhere else. Run it inside the Ubuntu test container.
//
// Usage (E1/E2, >=10 min, size=10MB backup=10):
//
//	go run ./test/soak -dur 10m  -size 10  -backups 10 -rate 2560 -label E
//
// Usage (G1, 4h, prod rate ~2-3 GB/h, size=100MB backup=10):
//
//	go run ./test/soak -dur 4h   -size 100 -backups 10 -rate 2560 -label G1
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	lumberjack "gopkg.in/natefinch/lumberjack.v2"

	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/source/tailer"
)

func main() {
	var (
		dir      = flag.String("dir", "", "log directory (default: a fresh dir under /tmp)")
		results  = flag.String("results", "test/results", "directory to archive CSV + summary into")
		label    = flag.String("label", "soak", "run label, used in output filenames")
		mode     = flag.String("mode", tailer.TailModeHybrid, "tail mode: hybrid/poll/event")
		dur      = flag.Duration("dur", 10*time.Minute, "total soak duration")
		interval = flag.Duration("interval", 30*time.Second, "sampling interval")
		warmup   = flag.Duration("warmup", 60*time.Second, "warmup window excluded from steady-state asserts")
		sizeMB   = flag.Int("size", 10, "lumberjack MaxSize in MB (rotate threshold)")
		backups  = flag.Int("backups", 10, "lumberjack MaxBackups (sliding window of live files)")
		rateMBh  = flag.Float64("rate", 2560, "write rate in MB/hour (2560 ~= 2.5 GB/h)")
		logLevel = flag.String("loglevel", "warn", "tailer log level (debug/info/warn/error) — keep high for long soaks")
	)
	flag.Parse()

	// Quiet the tailer's per-scan INFO logging; over a 4h soak it would dwarf the
	// sample lines and bloat the captured output.
	logging.Init(&logging.Config{Level: *logLevel})

	if runtime.GOOS != "linux" {
		fmt.Fprintln(os.Stderr, "soak: refusing to run off Linux — deleted-but-open fd accounting is a /proc/self/fd semantic")
		os.Exit(2)
	}

	logDir := *dir
	if logDir == "" {
		d, err := os.MkdirTemp("", "tango-soak-")
		must(err)
		logDir = d
	}
	must(os.MkdirAll(logDir, 0o755))
	must(os.MkdirAll(*results, 0o755))
	logPath := filepath.Join(logDir, "ta.test.log")
	glob := filepath.Join(logDir, "ta.test*.log")

	fmt.Printf("soak[%s]: dir=%s glob=%s mode=%s dur=%s size=%dMB backups=%d rate=%.0fMB/h interval=%s\n",
		*label, logDir, glob, *mode, *dur, *sizeMB, *backups, *rateMBh, *interval)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --- tailer: hybrid tail the glob, drain and discard (count only) ---
	tl := tailer.New([]string{glob}, 1*time.Second, *mode)
	out := tl.Run(ctx)
	var linesOut uint64
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		for range out {
			linesOut++
		}
	}()

	// --- writer: real lumberjack rotator at the target byte rate ---
	lj := &lumberjack.Logger{
		Filename:   logPath,
		MaxSize:    *sizeMB,
		MaxBackups: *backups,
		MaxAge:     0,
		Compress:   false,
	}
	bytesPerSec := *rateMBh * 1024 * 1024 / 3600
	var bytesWritten uint64
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		start := time.Now()
		seq := 0
		for ctx.Err() == nil {
			line := taLine(seq)
			seq++
			n, err := lj.Write(line)
			if err != nil {
				fmt.Fprintf(os.Stderr, "soak: write error: %v\n", err)
				return
			}
			bytesWritten += uint64(n)
			// Pace to the target rate: sleep until the ideal wall-clock time for
			// the bytes written so far. Compute nanoseconds in float space first
			// — time.Duration(fractionalSeconds) truncates to 0ns before any
			// multiply, which would disable pacing entirely.
			ideal := time.Duration(float64(bytesWritten) / bytesPerSec * float64(time.Second))
			if drift := ideal - time.Since(start); drift > 0 {
				select {
				case <-time.After(drift):
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	// --- sampler ---
	csvPath := filepath.Join(*results, fmt.Sprintf("soak_%s.csv", *label))
	csv, err := os.Create(csvPath)
	must(err)
	fmt.Fprintln(csv, "t_sec,deleted_fd,open_fd,goroutines,rss_kb,fs_used_mb,dir_mb,tailed,lines_out,mb_written")

	baseGoroutines := runtime.NumGoroutine()
	baseRSS := rssKB()
	deadline := time.Now().Add(*dur)
	start := time.Now()

	type sample struct {
		t                                     float64
		deletedFD, goroutines, tailed         int
		rssKB                                 int
		fsUsedMB, dirMB, mbWritten, linesOutF float64
	}
	var samples []sample

	tick := time.NewTicker(*interval)
	defer tick.Stop()
	// Take an immediate t=0 sample, then one per tick.
	for now := time.Now(); ; now = <-tick.C {
		dfd := countDeletedFDs(logDir)
		ofd := openFDCount()
		gor := runtime.NumGoroutine()
		rss := rssKB()
		used := fsUsedBytes(logDir)
		du := dirBytes(glob)
		tc := tl.TailedCount()
		s := sample{
			t:          now.Sub(start).Seconds(),
			deletedFD:  dfd,
			goroutines: gor,
			tailed:     tc,
			rssKB:      rss,
			fsUsedMB:   float64(used) / (1 << 20),
			dirMB:      float64(du) / (1 << 20),
			mbWritten:  float64(bytesWritten) / (1 << 20),
			linesOutF:  float64(linesOut),
		}
		samples = append(samples, s)
		fmt.Fprintf(csv, "%.0f,%d,%d,%d,%d,%.1f,%.1f,%d,%d,%.1f\n",
			s.t, dfd, ofd, gor, rss, s.fsUsedMB, s.dirMB, tc, linesOut, s.mbWritten)
		_ = csv.Sync()
		fmt.Printf("soak[%s] t=%5.0fs deleted_fd=%-3d goroutines=%-3d tailed=%-3d rss=%6dMB fs_used=%7.1fMB dir=%6.1fMB lines=%-9d written=%.1fMB\n",
			*label, s.t, dfd, gor, tc, rss/1024, s.fsUsedMB, s.dirMB, linesOut, s.mbWritten)

		if time.Now().After(deadline) {
			break
		}
	}

	// --- stop writer + tailer, drain ---
	cancel()
	<-writeDone
	_ = lj.Close()
	<-drainDone

	// --- invariants (steady state only) ---
	// Steady state begins after BOTH the configured warmup AND the time it takes
	// to fill the rotation window: until (backups+1) files of MaxSize each exist,
	// fs-used and dir-bytes ramp up monotonically (the window filling), which is
	// expected, not a leak. Excluding that ramp keeps the spread/trend bounds
	// meaningful — they then see only the post-fill sawtooth.
	fillSec := float64(*backups+1) * float64(*sizeMB) / (*rateMBh / 3600.0)
	steadyStart := warmup.Seconds()
	if fs := fillSec * 1.2; fs > steadyStart {
		steadyStart = fs
	}
	var steady []sample
	for _, s := range samples {
		if s.t >= steadyStart {
			steady = append(steady, s)
		}
	}
	if len(steady) < 2 {
		steady = samples // very short run; use everything
	}

	maxDeleted, maxTailed, maxGor := 0, 0, 0
	minUsed, maxUsed := steady[0].fsUsedMB, steady[0].fsUsedMB
	maxRSS := 0
	for _, s := range steady {
		maxi(&maxDeleted, s.deletedFD)
		maxi(&maxTailed, s.tailed)
		maxi(&maxGor, s.goroutines)
		maxi(&maxRSS, s.rssKB)
		if s.fsUsedMB < minUsed {
			minUsed = s.fsUsedMB
		}
		if s.fsUsedMB > maxUsed {
			maxUsed = s.fsUsedMB
		}
	}

	// Bounds. live files = current + backups; deleted fds should never exceed a
	// small multiple of that even transiently, and crucially must not scale with
	// run length. fs-used spread is bounded by a couple of rotation generations.
	liveFiles := *backups + 1
	deletedBound := liveFiles + 8
	tailedBound := liveFiles + 4
	usedSpreadBoundMB := float64(*sizeMB) * 3 // ≤ ~3 rotation generations of churn
	// Hybrid/event tailing runs ~3 goroutines per live file (our tail goroutine
	// plus hpcloud/tail's internal tailFileSync + watcher), so the steady count
	// scales with the live-file window, not with rotation count. Use a generous
	// ceiling and lean on the trend check below as the real leak signal.
	gorBound := baseGoroutines + 6*liveFiles + 16
	rssBoundKB := baseRSS*3 + 256*1024 // generous: ≤3x baseline + 256MB

	// "Not monotonic / not growing": a leak makes the metric climb with time, so
	// the last steady sample is the run-wide peak. Compare end-of-run to the
	// start of steady state — flat (or sawtooth) passes, monotonic growth fails.
	usedTrend := steady[len(steady)-1].fsUsedMB - steady[0].fsUsedMB
	gorTrend := steady[len(steady)-1].goroutines - steady[0].goroutines
	gorTrendBound := 2*liveFiles + 8

	type check struct {
		name string
		ok   bool
		msg  string
	}
	checks := []check{
		{"deleted_fd_bounded", maxDeleted <= deletedBound,
			fmt.Sprintf("max steady deleted_fd=%d (bound %d)", maxDeleted, deletedBound)},
		{"tailed_bounded", maxTailed <= tailedBound,
			fmt.Sprintf("max steady tailed=%d (bound %d)", maxTailed, tailedBound)},
		{"goroutines_bounded", maxGor <= gorBound,
			fmt.Sprintf("max steady goroutines=%d (bound %d, base %d)", maxGor, gorBound, baseGoroutines)},
		{"goroutines_not_growing", gorTrend <= gorTrendBound,
			fmt.Sprintf("goroutines end-start trend=%d (bound %d)", gorTrend, gorTrendBound)},
		{"rss_bounded", maxRSS <= rssBoundKB,
			fmt.Sprintf("max steady rss=%dMB (bound %dMB, base %dMB)", maxRSS/1024, rssBoundKB/1024, baseRSS/1024)},
		{"fs_used_spread_bounded", (maxUsed - minUsed) <= usedSpreadBoundMB,
			fmt.Sprintf("fs_used spread=%.1fMB [min %.1f, max %.1f] (bound %.1fMB)", maxUsed-minUsed, minUsed, maxUsed, usedSpreadBoundMB)},
		{"fs_used_not_monotonic", usedTrend <= usedSpreadBoundMB,
			fmt.Sprintf("fs_used end-start trend=%.1fMB (bound %.1fMB)", usedTrend, usedSpreadBoundMB)},
	}

	allOK := true
	var sb strings.Builder
	fmt.Fprintf(&sb, "=== soak[%s] summary ===\n", *label)
	fmt.Fprintf(&sb, "duration=%s mode=%s size=%dMB backups=%d rate=%.0fMB/h\n", *dur, *mode, *sizeMB, *backups, *rateMBh)
	fmt.Fprintf(&sb, "samples=%d steady=%d (steady starts @%.0fs, window-fill ~%.0fs) lines_out=%d mb_written=%.1f\n",
		len(samples), len(steady), steadyStart, fillSec, linesOut, float64(bytesWritten)/(1<<20))
	for _, c := range checks {
		status := "PASS"
		if !c.ok {
			status = "FAIL"
			allOK = false
		}
		fmt.Fprintf(&sb, "  [%s] %-24s %s\n", status, c.name, c.msg)
	}
	verdict := "PASS"
	if !allOK {
		verdict = "FAIL"
	}
	fmt.Fprintf(&sb, "VERDICT: %s\n", verdict)

	summary := sb.String()
	fmt.Print(summary)
	sumPath := filepath.Join(*results, fmt.Sprintf("soak_%s_summary.txt", *label))
	_ = os.WriteFile(sumPath, []byte(summary), 0o644)
	fmt.Printf("soak[%s]: csv=%s summary=%s\n", *label, csvPath, sumPath)

	if !allOK {
		os.Exit(1)
	}
}

// taLine builds a ~200-byte ThinkingData-shaped JSON log line.
func taLine(seq int) []byte {
	id := strconv.Itoa(seq)
	return []byte(`{"#type":"track","#event_name":"PaymentOrderState","#time":"2026-06-09 12:00:00.000","#uuid":"soak-` + id +
		`","#account_id":"acc-` + strconv.Itoa(seq%1000) + `","#distinct_id":"d-` + id +
		`","properties":{"seq":` + id + `,"amount":12.34,"state":"paid","sku":"item-` + strconv.Itoa(seq%50) + `"}}` + "\n")
}

func countDeletedFDs(dir string) int {
	const fdDir = "/proc/self/fd"
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil || !strings.HasSuffix(target, " (deleted)") {
			continue
		}
		if dir != "" && !strings.HasPrefix(strings.TrimSuffix(target, " (deleted)"), dir) {
			continue
		}
		n++
	}
	return n
}

func openFDCount() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

func rssKB() int {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(ln, "VmRSS:") {
			f := strings.Fields(ln)
			if len(f) >= 2 {
				v, _ := strconv.Atoi(f[1])
				return v
			}
		}
	}
	return -1
}

func fsUsedBytes(path string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0
	}
	return (st.Blocks - st.Bfree) * uint64(st.Bsize)
}

func dirBytes(glob string) uint64 {
	matches, _ := filepath.Glob(glob)
	var total uint64
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil {
			total += uint64(fi.Size())
		}
	}
	return total
}

func maxi(dst *int, v int) {
	if v > *dst {
		*dst = v
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "soak:", err)
		os.Exit(2)
	}
}
