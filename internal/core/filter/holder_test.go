package filter

import "testing"

func TestHolder_NilIsNoop(t *testing.T) {
	var h *Holder
	if !h.Empty() {
		t.Errorf("nil holder Empty()=false")
	}
	keep, err := h.Keep(map[string]any{"x": 1})
	if !keep || err != nil {
		t.Errorf("nil holder Keep=(%v,%v), want (true,nil)", keep, err)
	}
}

func TestHolder_HotSwap(t *testing.T) {
	h := NewHolder(nil)
	if !h.Empty() {
		t.Errorf("holder(nil).Empty()=false")
	}
	// Swap in a filter that only keeps #type=="track".
	f, err := New([]string{`#type == "track"`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	h.Store(f)
	if h.Empty() {
		t.Errorf("after Store, Empty()=true")
	}
	keep, _ := h.Keep(map[string]any{"#type": "track"})
	if !keep {
		t.Errorf("expected keep for #type=track")
	}
	keep, _ = h.Keep(map[string]any{"#type": "user_set"})
	if keep {
		t.Errorf("expected drop for #type=user_set")
	}
	// Swap back to no-op.
	h.Store(nil)
	keep, _ = h.Keep(map[string]any{"#type": "anything"})
	if !keep {
		t.Errorf("after Store(nil), expected keep-all")
	}
}
