package backfill

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// ProgressBar renders a single-line, in-place progress display to stderr when
// stderr is a TTY; otherwise it periodically emits a one-line summary so the
// progress is still visible in piped logs and containers.
//
// Counter sources:
//   - Days/chunks completed and total are managed explicitly by the runner.
//   - Per-day pages and pageCount are set when the runner observes the task
//     metadata.
//   - Row throughput is computed from the lines-ingested counter.
type ProgressBar struct {
	out       io.Writer
	isTTY     bool
	startTime time.Time
	lastTick  time.Time
	lastLines int64

	totalChunks  int32
	doneChunks   int32
	currentChunk atomic.Value // string
	currentPage  int32
	currentPages int32
	failedChunks int32
	totalLines   *atomic.Int64 // pointer to Stats.TotalLines
	mu           sync.Mutex
}

// NewProgressBar wires the bar to the given Stats counter for row throughput.
func NewProgressBar(stats *Stats, totalChunks int) *ProgressBar {
	pb := &ProgressBar{
		out:         os.Stderr,
		startTime:   time.Now(),
		lastTick:    time.Now(),
		totalLines:  &stats.TotalLines,
		totalChunks: int32(totalChunks),
	}
	pb.currentChunk.Store("")

	if f, ok := pb.out.(*os.File); ok {
		pb.isTTY = term.IsTerminal(int(f.Fd()))
	}
	return pb
}

// SetCurrentChunk records the chunk identifier the runner is now working on.
func (p *ProgressBar) SetCurrentChunk(chunkID string) {
	p.currentChunk.Store(chunkID)
	atomic.StoreInt32(&p.currentPage, 0)
	atomic.StoreInt32(&p.currentPages, 0)
}

// SetPageInfo updates the current chunk's page progress.
func (p *ProgressBar) SetPageInfo(pageID, pageCount int) {
	atomic.StoreInt32(&p.currentPage, int32(pageID+1)) // +1 → human-friendly 1..N
	atomic.StoreInt32(&p.currentPages, int32(pageCount))
}

// MarkChunkDone increments the completed-chunk counter.
func (p *ProgressBar) MarkChunkDone() { atomic.AddInt32(&p.doneChunks, 1) }

// MarkChunkFailed increments the failed-chunk counter.
func (p *ProgressBar) MarkChunkFailed() { atomic.AddInt32(&p.failedChunks, 1) }

// Render writes one update. In TTY mode the line is rewritten in place; in
// non-TTY mode it is a regular newline-terminated line.
func (p *ProgressBar) Render() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	lines := p.totalLines.Load()
	elapsed := now.Sub(p.startTime).Seconds()

	var lps float64
	if dt := now.Sub(p.lastTick).Seconds(); dt > 0 {
		lps = float64(lines-p.lastLines) / dt
	}
	p.lastTick = now
	p.lastLines = lines

	chunk, _ := p.currentChunk.Load().(string)
	done := atomic.LoadInt32(&p.doneChunks)
	total := p.totalChunks
	failed := atomic.LoadInt32(&p.failedChunks)
	page := atomic.LoadInt32(&p.currentPage)
	pages := atomic.LoadInt32(&p.currentPages)

	// Build the human-readable status line.
	line := fmt.Sprintf("[%d/%d chunks] cur=%s page=%d/%d rows=%s lps=%s elapsed=%s",
		done, total, displayChunk(chunk),
		page, pages,
		humanInt(lines),
		humanFloat(lps),
		shortDuration(time.Duration(elapsed)*time.Second))
	if failed > 0 {
		line += fmt.Sprintf(" failed=%d", failed)
	}

	if p.isTTY {
		// \r returns to column 0, \033[K clears to end of line, then we
		// write the new content without a trailing newline so the next
		// Render overwrites it.
		fmt.Fprintf(p.out, "\r\033[K%s", line)
		return
	}
	fmt.Fprintln(p.out, line)
}

// Finish writes a final newline (TTY mode only — keeps the last status line
// visible after the bar exits).
func (p *ProgressBar) Finish() {
	if p.isTTY {
		fmt.Fprintln(p.out)
	}
}

// StartTicker spawns a goroutine that calls Render() every interval until
// ctx is cancelled. Stop the ticker by cancelling ctx; the goroutine signals
// done by closing the returned channel.
func (p *ProgressBar) StartTicker(interval time.Duration, stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				p.Render()
				p.Finish()
				return
			case <-t.C:
				p.Render()
			}
		}
	}()
	return done
}

// ---------------------------------------------------------------------------
// formatting helpers
// ---------------------------------------------------------------------------

func displayChunk(c string) string {
	if c == "" {
		return "-"
	}
	return c
}

// humanInt formats an int64 with thousands separators using underscores
// (compact, easy to spot grouping in a single-line readout).
func humanInt(n int64) string {
	if n < 0 {
		return "-" + humanInt(-n)
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%s_%03d", humanInt(n/1000), n%1000)
}

func humanFloat(f float64) string {
	switch {
	case f >= 1e6:
		return fmt.Sprintf("%.1fM/s", f/1e6)
	case f >= 1e3:
		return fmt.Sprintf("%.1fk/s", f/1e3)
	default:
		return fmt.Sprintf("%.0f/s", f)
	}
}

func shortDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
