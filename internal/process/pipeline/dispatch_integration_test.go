package pipeline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Dispatch unit tests
// ---------------------------------------------------------------------------

func TestDispatch_RoutesToCorrectWorker(t *testing.T) {
	ctx := context.Background()
	n := 4
	workerChs := make([]chan string, n)
	for i := range workerChs {
		workerChs[i] = make(chan string, 100)
	}

	lineCh := make(chan string, 10)

	// Send lines with known routing keys
	lines := []string{
		`{"#account_id":"acc1","#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"d1"}`,
		`{"#account_id":"acc1","#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"d2"}`,
		`{"#account_id":"acc2","#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"d3"}`,
	}

	go func() {
		for _, line := range lines {
			lineCh <- line
		}
		close(lineCh)
	}()

	Dispatch(ctx, lineCh, workerChs)

	// Collect all dispatched lines
	total := 0
	for _, ch := range workerChs {
		total += len(ch)
	}

	if total != 3 {
		t.Errorf("expected 3 lines dispatched total, got %d", total)
	}

	// Lines with same account_id should go to the same worker
	idx1 := RouteIndex("acc1", n)
	if len(workerChs[idx1]) < 2 {
		t.Errorf("expected at least 2 lines for acc1's worker (idx=%d), got %d", idx1, len(workerChs[idx1]))
	}
}

func TestDispatch_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n := 2
	workerChs := make([]chan string, n)
	for i := range workerChs {
		workerChs[i] = make(chan string, 1) // small buffer
	}

	lineCh := make(chan string, 100)

	// Fill lineCh with many lines
	for i := 0; i < 50; i++ {
		lineCh <- fmt.Sprintf(`{"#account_id":"acc%d","#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"ctx-%d"}`, i, i)
	}

	// Cancel quickly
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	// Dispatch should return eventually (not hang)
	done := make(chan struct{})
	go func() {
		Dispatch(ctx, lineCh, workerChs)
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch did not return after context cancellation")
	}
}

func TestDispatch_EmptyInput(t *testing.T) {
	ctx := context.Background()
	n := 2
	workerChs := make([]chan string, n)
	for i := range workerChs {
		workerChs[i] = make(chan string, 10)
	}

	lineCh := make(chan string)
	close(lineCh) // immediately close

	Dispatch(ctx, lineCh, workerChs)

	total := 0
	for _, ch := range workerChs {
		total += len(ch)
	}
	if total != 0 {
		t.Errorf("expected 0 lines, got %d", total)
	}
}

func TestDispatch_EnvelopeRouting(t *testing.T) {
	ctx := context.Background()
	n := 4
	workerChs := make([]chan string, n)
	for i := range workerChs {
		workerChs[i] = make(chan string, 100)
	}

	lineCh := make(chan string, 10)

	inner := `{"#account_id":"env_acc","#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"env-d1"}`
	envelope := `{"msg":"` + strings.ReplaceAll(inner, `"`, `\"`) + `"}`

	go func() {
		lineCh <- envelope
		close(lineCh)
	}()

	Dispatch(ctx, lineCh, workerChs)

	// Should route based on extracted key from envelope
	expectedIdx := RouteIndex("env_acc", n)
	if len(workerChs[expectedIdx]) != 1 {
		t.Errorf("expected 1 line at worker %d, got %d", expectedIdx, len(workerChs[expectedIdx]))
	}
}

func TestDispatch_AffinityGuarantee(t *testing.T) {
	ctx := context.Background()
	n := 8
	workerChs := make([]chan string, n)
	for i := range workerChs {
		workerChs[i] = make(chan string, 1000)
	}

	lineCh := make(chan string, 1000)

	// Generate many lines for the same account: should all go to the same worker
	acc := "affinity_test_acc"
	count := 100
	go func() {
		for i := 0; i < count; i++ {
			lineCh <- fmt.Sprintf(`{"#account_id":"%s","#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"aff-%d"}`, acc, i)
		}
		close(lineCh)
	}()

	Dispatch(ctx, lineCh, workerChs)

	expectedIdx := RouteIndex(acc, n)
	if len(workerChs[expectedIdx]) != count {
		t.Errorf("expected all %d lines at worker %d, got %d", count, expectedIdx, len(workerChs[expectedIdx]))
	}

	// All other workers should be empty
	for i, ch := range workerChs {
		if i != expectedIdx && len(ch) != 0 {
			t.Errorf("expected 0 lines at worker %d, got %d", i, len(ch))
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrent dispatch
// ---------------------------------------------------------------------------

func TestDispatch_ConcurrentProducers(t *testing.T) {
	ctx := context.Background()
	n := 4
	workerChs := make([]chan string, n)
	for i := range workerChs {
		workerChs[i] = make(chan string, 1000)
	}

	lineCh := make(chan string, 1000)
	producerCount := 10
	linesPerProducer := 50

	var wg sync.WaitGroup
	wg.Add(producerCount)
	for p := 0; p < producerCount; p++ {
		go func(pid int) {
			defer wg.Done()
			for i := 0; i < linesPerProducer; i++ {
				lineCh <- fmt.Sprintf(`{"#account_id":"prod_%d","#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"conc-%d-%d"}`, pid, pid, i)
			}
		}(p)
	}

	// Close lineCh after all producers finish
	go func() {
		wg.Wait()
		close(lineCh)
	}()

	Dispatch(ctx, lineCh, workerChs)

	total := 0
	for _, ch := range workerChs {
		total += len(ch)
	}

	expected := producerCount * linesPerProducer
	if total != expected {
		t.Errorf("expected %d total lines, got %d", expected, total)
	}
}

// ---------------------------------------------------------------------------
// Test for head-of-line blocking prevention
// ---------------------------------------------------------------------------

func TestDispatch_NoBlockWhenWorkerChannelFull(t *testing.T) {
	ctx := context.Background()
	n := 4
	workerChs := make([]chan string, n)
	for i := range workerChs {
		workerChs[i] = make(chan string, 5)
	}

	lineCh := make(chan string, 100)

	// Pre-fill worker 0's channel to simulate a slow worker (e.g. MongoDB flush).
	for range 5 {
		workerChs[0] <- "stuck"
	}

	// Drain workers 1,2,3 so they always accept lines (simulating healthy workers).
	var wg sync.WaitGroup
	for i := 1; i < n; i++ {
		wg.Add(1)
		go func(ch <-chan string) {
			defer wg.Done()
			for range ch {
			}
		}(workerChs[i])
	}

	// Generate lines that all route to worker 0. The fixed dispatcher
	// should spill them to workers 1/2/3 instead of blocking.
	totalLines := 50
	go func() {
		for i := 0; i < totalLines; i++ {
			lineCh <- fmt.Sprintf(`{"#account_id":"blocked_user","#type":"track","#event_name":"e","#time":"2024-01-01","#uuid":"blk-%d"}`, i)
		}
		close(lineCh)
	}()

	done := make(chan struct{})
	go func() {
		Dispatch(ctx, lineCh, workerChs)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Dispatch blocked when all lines route to a full worker — head-of-line blocking")
	}

	wg.Wait()
}
