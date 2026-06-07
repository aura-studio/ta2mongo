package cfgsync

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/logging"
)

// reservedFields are the config-document fields that carry the document's
// identity/versioning rather than a tunable subtree, so the registry never
// routes them to an applier.
var reservedFields = map[string]struct{}{
	"_id":     {},
	"version": {},
}

// SubtreeFilter is the document subtree carrying the reporting filter
// (`filter: {include, exclude}`). It is the one applier registered by default and
// maps to the parser's runtime filter (the `parser.filter.*` reporting rules in
// the static config). It is the gold-standard dynamic key: applied via an atomic
// Holder swap on the data path without touching any resource identity.
const SubtreeFilter = "filter"

// ApplyFunc applies one subtree of a config document at runtime. It must
// validate before mutating and, on failure, leave the live value untouched
// (last-good) and return an error — that is the second of the two gates (the
// publish side is the first) that keep a bad config from taking down the process.
type ApplyFunc func(sub bson.M) error

// Registry is the allowlist of dynamically-reconfigurable subtrees: a map from
// document subtree name to its applier. It is the generalisation of a single
// onChange callback into a bounded, auditable set of dynamic keys. A subtree with
// no registered applier is, by definition, not in the allowlist: the Watcher
// refuses to apply it (logs a warning) rather than silently ignoring it.
//
// Deliberately small by default (only SubtreeFilter): the more a remote document
// can reconfigure, the larger the blast radius, so the dynamic surface is kept
// minimal on purpose. Adding a key means registering a "validate + atomic apply"
// function that satisfies the live-reconfigure criteria documented in
// doc/cfgsync.TODO.md.
type Registry struct {
	appliers map[string]ApplyFunc
}

// NewRegistry returns an empty Registry. Callers register the subtrees they
// support (typically just SubtreeFilter via RegisterFilter).
func NewRegistry() *Registry {
	return &Registry{appliers: make(map[string]ApplyFunc)}
}

// Register adds (or replaces) the applier for a subtree, putting that subtree on
// the allowlist.
func (r *Registry) Register(subtree string, fn ApplyFunc) {
	r.appliers[subtree] = fn
}

// Allows reports whether subtree is on the allowlist (has a registered applier).
// It is the shared predicate behind both apply-side routing and publish-side
// validation.
func (r *Registry) Allows(subtree string) bool {
	_, ok := r.appliers[subtree]
	return ok
}

// Apply routes each non-reserved field of doc to its registered applier. It is
// the Watcher's onChange: a field on the allowlist is handed to its applier;
// a field with no applier is rejected with a warning (never silently applied).
// The first applier error is returned (after attempting the rest) so the caller
// can count it; appliers that fail leave their live value at last-good.
func (r *Registry) Apply(doc bson.M) error {
	var firstErr error
	for key, val := range doc {
		if _, reserved := reservedFields[key]; reserved {
			continue
		}
		fn, ok := r.appliers[key]
		if !ok {
			logging.WithField("subtree", key).
				Warn("cfgsync: ignoring config subtree not on allowlist")
			if firstErr == nil {
				firstErr = fmt.Errorf("cfgsync: subtree %q is not on the allowlist", key)
			}
			continue
		}
		sub, err := asSubDocument(key, val)
		if err != nil {
			logging.WithError(err).WithField("subtree", key).
				Warn("cfgsync: malformed config subtree, keeping last-good")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := fn(sub); err != nil {
			logging.WithError(err).WithField("subtree", key).
				Warn("cfgsync: applier rejected config subtree, keeping last-good")
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// asSubDocument coerces a document field value into a bson.M. DocumentDB / the
// driver may decode a nested document as bson.M or bson.D depending on the path,
// so both are accepted.
func asSubDocument(key string, val any) (bson.M, error) {
	switch v := val.(type) {
	case bson.M:
		return v, nil
	case bson.D:
		m := make(bson.M, len(v))
		for _, e := range v {
			m[e.Key] = e.Value
		}
		return m, nil
	case map[string]any:
		return bson.M(v), nil
	default:
		return nil, fmt.Errorf("cfgsync: subtree %q is not a document (got %T)", key, val)
	}
}
