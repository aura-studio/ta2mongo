package cfgsync

import (
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/parser"
)

// TestFilterHotSwap_RaceFree asserts the cfgsync apply path (RegisterFilter →
// parser.SwapFilter → Holder.Store) is safe to run concurrently with live
// reporting (Holder.Keep on the data path). The Holder is an atomic.Pointer, so
// a swap mid-report must neither tear nor data-race. Run under `go test -race`;
// the assertion is the absence of a race report plus every Keep returning a
// well-defined (non-erroring) result against whichever filter happens to be live.
func TestFilterHotSwap_RaceFree(t *testing.T) {
	p := parser.New(nil)
	reg := NewRegistry()
	RegisterFilter(reg, p)

	// Two valid, alternating filter documents — both compile, so every swap
	// installs a working filter and Keep always has a live filter to consult.
	docs := []bson.M{
		{"version": int64(0), "filter": bson.M{"include": bson.A{`#type == "track"`}}},
		{"version": int64(0), "filter": bson.M{"include": bson.A{`#type == "user_set"`}}},
	}

	env := map[string]any{"#type": "track", "#event_name": "login"}

	const swappers, readers, iters = 2, 8, 2000
	var wg sync.WaitGroup

	for s := 0; s < swappers; s++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if err := reg.Apply(docs[(seed+i)%len(docs)]); err != nil {
					t.Errorf("apply: %v", err)
					return
				}
			}
		}(s)
	}

	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				if _, err := p.Filter().Keep(env); err != nil {
					t.Errorf("keep: %v", err)
					return
				}
			}
		}()
	}

	wg.Wait()
}
