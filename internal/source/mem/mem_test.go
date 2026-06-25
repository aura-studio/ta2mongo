package mem

import (
	"context"
	"errors"
	"testing"
	"time"
)

// drain reads all lines from the source channel until it closes.
func drain(s *Source) []string {
	var got []string
	for line := range s.Run(context.Background()) {
		got = append(got, line)
	}
	return got
}

func TestPushThenCloseDrainsInOrder(t *testing.T) {
	s := New(8)
	done := make(chan []string, 1)
	go func() { done <- drain(s) }()

	ctx := context.Background()
	for _, line := range []string{"a", "b", "c"} {
		if err := s.Push(ctx, line); err != nil {
			t.Fatalf("push %q: %v", line, err)
		}
	}
	s.Close()

	got := <-done
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("got %v, want [a b c] in order", got)
	}
}

func TestEmptyLinesSkipped(t *testing.T) {
	s := New(8)
	done := make(chan []string, 1)
	go func() { done <- drain(s) }()

	ctx := context.Background()
	for _, line := range []string{"", "x", "", "y", ""} {
		if err := s.Push(ctx, line); err != nil {
			t.Fatalf("push: %v", err)
		}
	}
	s.Close()

	got := <-done
	if len(got) != 2 || got[0] != "x" || got[1] != "y" {
		t.Fatalf("got %v, want [x y]", got)
	}
}

func TestCloseClosesChannel(t *testing.T) {
	s := New(4)
	s.Close()
	// Draining a closed, empty source yields nothing and returns promptly.
	got := drain(s)
	if len(got) != 0 {
		t.Fatalf("got %v, want empty", got)
	}
}

func TestPushAfterCloseReturnsErrClosed(t *testing.T) {
	s := New(4)
	s.Close()
	if err := s.Push(context.Background(), "late"); !errors.Is(err, ErrClosed) {
		t.Fatalf("push after close: got %v, want ErrClosed", err)
	}
}

func TestPushBlocksThenCtxCancel(t *testing.T) {
	s := New(1) // capacity 1
	ctx := context.Background()
	if err := s.Push(ctx, "fills-buffer"); err != nil {
		t.Fatalf("first push: %v", err)
	}
	// Buffer is full; a second push must block until ctx is cancelled.
	cctx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)
	go func() { errCh <- s.Push(cctx, "would-block") }()

	select {
	case err := <-errCh:
		t.Fatalf("push returned %v while buffer full; expected to block", err)
	case <-time.After(50 * time.Millisecond):
		// still blocked, as expected
	}
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked push: got %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked push did not return after ctx cancel")
	}
}

func TestConcurrentProduceConsume(t *testing.T) {
	s := New(4) // small buffer forces interleaving (backpressure)
	const n = 1000
	done := make(chan int, 1)
	go func() {
		count := 0
		for range s.Run(context.Background()) {
			count++
		}
		done <- count
	}()

	ctx := context.Background()
	for i := 0; i < n; i++ {
		if err := s.Push(ctx, "line"); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	s.Close()

	if got := <-done; got != n {
		t.Fatalf("consumed %d, want %d", got, n)
	}
}
