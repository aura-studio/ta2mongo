package cfgsync

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/parser"
)

// RegisterFilter puts the reporting-filter applier on the registry's allowlist:
// the SubtreeFilter subtree (`filter: {include, exclude}`) is parsed and pushed
// onto the parser's live filter via the parser root façade (SwapFilter), which
// compiles-then-atomically-swaps and leaves the last-good filter in place on a
// compile error. This is the one applier the long-running roles (daemon, gateway)
// register by default; it reaches the parser only through its root package, never
// parser/filter.
//
// cfgsync stays filter-agnostic itself — the Watcher only fetches + version-guards
// + invokes the registry; this is the single place that knows the document's
// filter shape maps to parser.SwapFilter.
func RegisterFilter(reg *Registry, p *parser.Parser) {
	reg.Register(SubtreeFilter, func(sub bson.M) error {
		include, err := toStringSlice(sub["include"])
		if err != nil {
			return fmt.Errorf("cfgsync: filter.include: %w", err)
		}
		exclude, err := toStringSlice(sub["exclude"])
		if err != nil {
			return fmt.Errorf("cfgsync: filter.exclude: %w", err)
		}
		return p.SwapFilter(include, exclude)
	})
}

// toStringSlice coerces a config-document array field into []string. A missing
// field (nil) is an empty list. BSON arrays decode as bson.A ([]any) over the
// wire and as []string when set in-process (tests), so both are accepted; any
// non-string element is an error so a malformed rule is rejected (last-good kept)
// rather than silently dropped.
func toStringSlice(v any) ([]string, error) {
	switch a := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return a, nil
	case bson.A:
		out := make([]string, 0, len(a))
		for i, e := range a {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is not a string (got %T)", i, e)
			}
			out = append(out, s)
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(a))
		for i, e := range a {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("element %d is not a string (got %T)", i, e)
			}
			out = append(out, s)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("expected an array, got %T", v)
	}
}
