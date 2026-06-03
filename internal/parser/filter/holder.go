package filter

import "sync/atomic"

// Holder wraps a *Filter behind an atomic pointer so the active filter can be
// hot-swapped at runtime (e.g. when a remote config update arrives) while
// worker goroutines keep calling Keep/Empty on the hot path without locks.
//
// A nil *Holder behaves as a no-op (keeps everything), mirroring *Filter, so
// callers can pass nil where a static no-op filter is acceptable.
type Holder struct {
	p atomic.Pointer[Filter]
}

// NewHolder returns a Holder initialised with f (which may be nil).
func NewHolder(f *Filter) *Holder {
	h := &Holder{}
	h.p.Store(f)
	return h
}

// Store atomically replaces the active filter.
func (h *Holder) Store(f *Filter) { h.p.Store(f) }

// Current returns the active filter (possibly nil).
func (h *Holder) Current() *Filter {
	if h == nil {
		return nil
	}
	return h.p.Load()
}

// Keep delegates to the active filter. A nil holder or nil active filter keeps
// every record, matching Filter.Keep semantics.
func (h *Holder) Keep(env map[string]any) (bool, error) {
	return h.Current().Keep(env)
}

// Empty reports whether the active filter has no rules.
func (h *Holder) Empty() bool {
	return h.Current().Empty()
}
