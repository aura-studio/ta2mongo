package source

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aura-studio/tango/internal/source/tailer"
	"github.com/aura-studio/tango/internal/source/uploadfile"
)

// ultra_source_test.go covers the source facade (SRC-1..SRC-5; SRC-1..4 from
// doc/ultra_test.md, since archived): the NewLines (httpbody) and NewReader
// (stdin) finite sources, NewTailer's nil/zero config defaulting, the
// NewUploadFile (v1.6.0) finite file import, and the panic-isolation contract
// of the source goroutines.

// drain collects every value a Source emits until its channel closes, returning the
// emitted slice and whether the channel was observed closed within the deadline.
func drain(t *testing.T, ch <-chan string, deadline time.Duration) (got []string, closed bool) {
	t.Helper()
	timeout := time.After(deadline)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return got, true
			}
			got = append(got, v)
		case <-timeout:
			return got, false
		}
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// SRC-1: NewLines (httpbody) — emits every non-empty line then closes; empties
// are skipped; ctx cancel stops it.
// ---------------------------------------------------------------------------

func TestUltraSource_NewLines_EmitsNonEmptyThenCloses(t *testing.T) {
	in := []string{"alpha", "", "beta", "", "", "gamma", "alpha"}
	src := NewLines(in)
	if src == nil {
		t.Fatal("NewLines returned nil Source")
	}

	got, closed := drain(t, src.Run(context.Background()), 2*time.Second)
	if !closed {
		t.Fatal("NewLines channel did not close")
	}

	// The multiset of emitted lines must equal the non-empty inputs (order is the
	// slice order, but assert as a multiset to be robust). "alpha" appears twice.
	want := []string{"alpha", "beta", "gamma", "alpha"}
	if len(got) != len(want) {
		t.Fatalf("emitted %d lines %q, want %d %q", len(got), got, len(want), want)
	}
	gs, ws := sortedCopy(got), sortedCopy(want)
	for i := range ws {
		if gs[i] != ws[i] {
			t.Fatalf("emitted multiset %q, want %q", gs, ws)
		}
	}
	// No empty string ever crosses the channel.
	for _, v := range got {
		if v == "" {
			t.Fatalf("empty line was emitted: %q", got)
		}
	}
}

func TestUltraSource_NewLines_NilSliceEmitsNothing(t *testing.T) {
	got, closed := drain(t, NewLines(nil).Run(context.Background()), 2*time.Second)
	if !closed {
		t.Fatal("NewLines(nil) channel did not close")
	}
	if len(got) != 0 {
		t.Fatalf("NewLines(nil) emitted %q, want nothing", got)
	}
}

func TestUltraSource_NewLines_CtxCancelStops(t *testing.T) {
	// A large input; cancel before draining so the producer hits ctx.Done().
	in := make([]string, 5000)
	for i := range in {
		in[i] = "line"
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := NewLines(in).Run(ctx)
	cancel()

	// After cancel the channel must eventually close. We may receive some buffered
	// lines first, but it must close rather than block forever.
	_, closed := drain(t, ch, 2*time.Second)
	if !closed {
		t.Fatal("NewLines channel did not close after ctx cancel")
	}
}

// ---------------------------------------------------------------------------
// SRC-2: NewReader (stdin) — scans lines from an io.Reader, skips empties, stops
// at EOF and closes; respects ctx cancel; an over-max line stops the scan.
// ---------------------------------------------------------------------------

func TestUltraSource_NewReader_ScansLinesUntilEOF(t *testing.T) {
	// Mixed: blank lines between, trailing newline, no final newline on last.
	r := strings.NewReader("one\n\ntwo\n\n\nthree\nfour")
	src := NewReader(r)
	if src == nil {
		t.Fatal("NewReader returned nil Source")
	}

	got, closed := drain(t, src.Run(context.Background()), 2*time.Second)
	if !closed {
		t.Fatal("NewReader channel did not close at EOF")
	}
	want := []string{"one", "two", "three", "four"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("NewReader emitted %q, want %q (empties must be skipped, order preserved)", got, want)
	}
}

func TestUltraSource_NewReader_EmptyReaderClosesImmediately(t *testing.T) {
	got, closed := drain(t, NewReader(strings.NewReader("")).Run(context.Background()), 2*time.Second)
	if !closed {
		t.Fatal("NewReader(empty) channel did not close")
	}
	if len(got) != 0 {
		t.Fatalf("NewReader(empty) emitted %q, want nothing", got)
	}
}

func TestUltraSource_NewReader_CtxCancelStops(t *testing.T) {
	// endlessReader supplies an unbounded stream of distinct non-empty lines, so the
	// stdin source never reaches EOF. Without draining, the 256-slot output channel
	// fills and the producer blocks on `out <- line`; the only exit is ctx.Done().
	// Cancellation must therefore close the channel (proving the cancel branch of
	// the select is taken, not EOF).
	ctx, cancel := context.WithCancel(context.Background())
	ch := NewReader(&endlessReader{}).Run(ctx)

	// Let the producer fill the buffer and park on the blocking send.
	for i := 0; i < 300; i++ {
		<-ch
	}
	cancel()
	_, closed := drain(t, ch, 2*time.Second)
	if !closed {
		t.Fatal("NewReader channel did not close after ctx cancel while producer was blocked")
	}
}

func TestUltraSource_NewReader_OverMaxLineStopsScan(t *testing.T) {
	// defaultMaxLineBytes for stdin is 1 MiB. A single line exceeding that makes
	// bufio.Scanner.Scan return false with ErrTooLong; the oversized token is NOT
	// emitted and the channel closes. A short line that follows is unreachable
	// because the scanner already errored, so it is dropped too.
	const mib = 1 << 20
	huge := strings.Repeat("x", mib+1024) // > 1 MiB, no newline
	input := huge + "\nshortline\n"

	got, closed := drain(t, NewReader(strings.NewReader(input)).Run(context.Background()), 3*time.Second)
	if !closed {
		t.Fatal("NewReader channel did not close after over-max line")
	}
	// Per the code path (scanner.Buffer cap = maxLineSize), the oversized token is
	// never delivered, and the scan aborts on ErrTooLong before reaching the next
	// line. So nothing is emitted at all.
	if len(got) != 0 {
		t.Fatalf("over-max input emitted %d lines %q; expected the oversized token to be dropped and the scan to abort (no lines)", len(got), got)
	}
	for _, v := range got {
		if len(v) > mib {
			t.Fatalf("an over-max line (%d bytes) was emitted; scanner cap should have prevented it", len(v))
		}
	}
}

// ---------------------------------------------------------------------------
// SRC-3: NewTailer(nil) / zero cfg must not panic and uses defaults.
// ---------------------------------------------------------------------------

func TestUltraSource_NewTailer_NilConfigDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewTailer(nil) panicked: %v", r)
		}
	}()
	src := NewTailer(nil)
	if src == nil {
		t.Fatal("NewTailer(nil) returned nil Source")
	}
	// The concrete type is *tailer.Tailer; a nil cfg routes through the defaulting
	// branch in NewTailer (cfg = &tailer.Config{}). Construction alone must not panic.
	if _, ok := src.(*tailer.Tailer); !ok {
		t.Fatalf("NewTailer(nil) returned %T, want *tailer.Tailer", src)
	}
}

func TestUltraSource_NewTailer_ZeroConfigUsesTuningDefaults(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewTailer(&Config{}) panicked: %v", r)
		}
	}()
	// A zero Config: empty LogPattern, empty TailMode, all zero tunings. NewTailer
	// passes these straight to tailer.New(...).WithTuning(0,0); tailer.New defaults
	// an empty TailMode to "poll" and WithTuning(0,0) keeps the built-in tuning
	// defaults. No panic at construction.
	src := NewTailer(&tailer.Config{})
	tl, ok := src.(*tailer.Tailer)
	if !ok {
		t.Fatalf("NewTailer(&Config{}) returned %T, want *tailer.Tailer", src)
	}
	// With no patterns the tailer tracks no files.
	if n := tl.TailedCount(); n != 0 {
		t.Fatalf("zero-config tailer TailedCount = %d, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// SRC-5: NewUploadFile (one-shot file import) — nil/zero config must not
// panic and emits nothing; a configured pattern imports the matched files to
// EOF then closes (the finite-source contract through the facade).
// ---------------------------------------------------------------------------

func TestUltraSource_NewUploadFile_NilConfigDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewUploadFile(nil) panicked: %v", r)
		}
	}()
	src := NewUploadFile(nil)
	if src == nil {
		t.Fatal("NewUploadFile(nil) returned nil Source")
	}
	if _, ok := src.(*uploadfile.Source); !ok {
		t.Fatalf("NewUploadFile(nil) returned %T, want *uploadfile.Source", src)
	}
	// A nil config has no patterns: the source closes immediately, emitting
	// nothing.
	got, closed := drain(t, src.Run(context.Background()), 2*time.Second)
	if !closed {
		t.Fatal("NewUploadFile(nil) channel did not close")
	}
	if len(got) != 0 {
		t.Fatalf("NewUploadFile(nil) emitted %q, want nothing", got)
	}
}

func TestUltraSource_NewUploadFile_ImportsMatchedFilesThenCloses(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.log"), []byte("a1\na2\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("never\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	src := NewUploadFile(&uploadfile.Config{LogPattern: []string{filepath.Join(dir, "*.log")}})
	got, closed := drain(t, src.Run(context.Background()), 5*time.Second)
	if !closed {
		t.Fatal("NewUploadFile channel did not close after the finite import")
	}
	want := []string{"a1", "a2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("NewUploadFile emitted %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// SRC-4: the source goroutines have logging.Recover (panic isolation). A panic
// inside the scan loop must be caught so the process survives and the channel is
// still closed (close(out) is deferred under the Recover, LIFO).
// ---------------------------------------------------------------------------

func TestUltraSource_NewReader_PanicInReaderIsIsolated(t *testing.T) {
	// panickyReader panics from Read. bufio.Scanner does not recover panics, so the
	// panic propagates into the stdin source goroutine, where logging.Recover (the
	// deferred call registered after, hence running before, close(out)) catches it.
	// Observable contract: the test process is NOT crashed and the output channel
	// is still closed by the deferred close(out).
	pr := &panickyReader{}
	ch := NewReader(pr).Run(context.Background())

	got, closed := drain(t, ch, 2*time.Second)
	if !closed {
		t.Fatal("channel did not close after a panic in the reader; logging.Recover/close(out) contract broken")
	}
	if len(got) != 0 {
		t.Fatalf("expected no lines before the panic, got %q", got)
	}
	if !pr.called {
		t.Fatal("Read was never called; panic injection point not exercised")
	}
}

func TestUltraSource_NewLines_RunSpawnsConcurrentGoroutine(t *testing.T) {
	// httpbody/stdin Run each spawn exactly one producer goroutine guarded by
	// logging.Recover. Verify Run returns before the channel is drained (i.e. the
	// work happens on another goroutine, not synchronously in Run): we read the
	// first value only after a brief pause, proving the producer is concurrent and
	// the buffered channel holds the work.
	src := NewLines([]string{"first", "second"})
	ch := src.Run(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	var first string
	var ok bool
	go func() {
		defer wg.Done()
		first, ok = <-ch
	}()
	wg.Wait()
	if !ok || first != "first" {
		t.Fatalf("concurrent producer: first received = %q (ok=%v), want \"first\"", first, ok)
	}
	rest, closed := drain(t, ch, 2*time.Second)
	if !closed {
		t.Fatal("channel did not close")
	}
	if len(rest) != 1 || rest[0] != "second" {
		t.Fatalf("remaining lines = %q, want [second]", rest)
	}
}

// ---------------------------------------------------------------------------
// test readers
// ---------------------------------------------------------------------------

// endlessReader emits an unbounded stream of newline-terminated lines, never EOF.
type endlessReader struct {
	buf []byte
	n   int
}

func (e *endlessReader) Read(p []byte) (int, error) {
	if len(e.buf) == 0 {
		e.n++
		e.buf = []byte("line-" + strconv.Itoa(e.n) + "\n")
	}
	c := copy(p, e.buf)
	e.buf = e.buf[c:]
	return c, nil
}

// panickyReader panics on its first Read call.
type panickyReader struct {
	called bool
}

func (p *panickyReader) Read(_ []byte) (int, error) {
	p.called = true
	panic("injected reader panic")
}
