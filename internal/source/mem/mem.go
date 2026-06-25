// Package mem is an in-memory relay source: a producer pushes already-formed TA
// log lines into it while the process pipeline drains them concurrently. It is
// the in-memory analogue of source/file — file reads lines from disk, mem
// receives them from a producer in the same process — and satisfies the
// source.Source contract consumed by the process uploaders.
//
// Unlike source/httpbody (a fixed pre-built slice), mem is a conduit: Run hands
// the pipeline a receive channel, the producer streams lines in via Push as it
// generates them, and Close ends the stream so the pipeline finishes. This lets
// a producer (e.g. the backfill domain, fetching ThinkingData rows page by
// page) feed the normal parse → filter → identity → write pipeline without
// buffering everything first.
//
// Usage (single producer):
//
//	m := mem.New(2000)
//	go func() { res, err = engine.Run(ctx, m) }() // consumer drains m
//	for _, line := range produced {
//		if err := m.Push(ctx, line); err != nil { break }
//	}
//	m.Close() // pipeline drains the remainder and Run returns
//
// A Source has a SINGLE producer: Push is not safe for concurrent callers, and
// Close must be called by that same producer after its last Push. Consumption
// (Run's channel) is independent and may run in another goroutine.
package mem

import (
	"context"
	"errors"
	"sync"
)

// defaultBuffer is the channel capacity used when New is given a non-positive
// size. It bounds how far the producer may run ahead of the pipeline (Push
// blocks once full, applying backpressure).
const defaultBuffer = 2000

// ErrClosed is returned by Push after Close has been called.
var ErrClosed = errors.New("mem: push after close")

// Source is an in-memory relay implementing source.Source. The producer feeds
// lines with Push and ends the stream with Close; the pipeline drains Run's
// channel until it closes.
type Source struct {
	ch       chan string
	closed   chan struct{}
	closeOne sync.Once
}

// New creates a relay Source whose internal channel buffers up to buf lines
// (defaultBuffer when buf <= 0).
func New(buf int) *Source {
	if buf <= 0 {
		buf = defaultBuffer
	}
	return &Source{
		ch:     make(chan string, buf),
		closed: make(chan struct{}),
	}
}

// Push enqueues one non-empty line for the pipeline, blocking while the buffer
// is full (backpressure). Empty lines are skipped. It returns ctx.Err() if ctx
// is cancelled while blocked, or ErrClosed if the source is already closed.
// Single-producer only.
func (s *Source) Push(ctx context.Context, line string) error {
	if line == "" {
		return nil
	}
	select {
	case <-s.closed:
		return ErrClosed
	default:
	}
	select {
	case s.ch <- line:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.closed:
		return ErrClosed
	}
}

// Close ends the stream: the producer calls it after its last Push so the
// pipeline drains the buffered lines and Run's channel closes. Idempotent and
// safe to call once from the producer goroutine (which is not concurrently in
// Push, per the single-producer contract).
func (s *Source) Close() {
	s.closeOne.Do(func() {
		close(s.closed)
		close(s.ch)
	})
}

// Run returns the receive channel the pipeline drains. It does not start any
// goroutine — production is external (Push) — and the channel closes when the
// producer calls Close (or stays open until then). ctx is accepted to satisfy
// the source.Source contract; cancellation is handled producer-side via Push.
func (s *Source) Run(ctx context.Context) <-chan string {
	return s.ch
}
