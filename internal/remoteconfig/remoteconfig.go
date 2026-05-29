// Package remoteconfig implements the MongoDB-backed configuration override.
//
// A single document in a known collection holds a JSON/BSON object whose
// fields mirror the local YAML config keys (mapstructure tags). At startup —
// and, in daemon mode, periodically — the document is fetched and merged on
// top of the local configuration on a per-field basis: only the keys present
// in the document override the local values; everything else is preserved.
//
// Connection-level fields are never overridable from the remote document, by
// design: the client must already be connected (using local mongoURI) to read
// the document in the first place, and letting the control plane rewrite its
// own location or the DB endpoint would be a foot-gun.
//
// Scope: the override applies per database. The document lives in the
// _tango_config collection of whichever database the tango instance connects
// to; it only affects instances pointed at that same database. A different
// database is an independent namespace managing its own set of tango
// processes with its own override document.
package remoteconfig

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-viper/mapstructure/v2"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
)

// forbiddenKeys are top-level config keys that the remote document may not
// override. They are silently dropped from the fetched document before merge.
var forbiddenKeys = map[string]struct{}{
	"mongoURI":     {},
	"remoteConfig": {},
	"mode":         {},
}

// Fetch loads the override document by _id from the given collection. It
// returns (nil, nil) when no such document exists — an absent document simply
// means "no override", not an error.
func Fetch(ctx context.Context, coll *mongo.Collection, documentID string) (map[string]any, error) {
	var raw bson.M
	err := coll.FindOne(ctx, bson.M{"_id": documentID}).Decode(&raw)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("remoteconfig: fetch %q: %w", documentID, err)
	}
	// Drop the _id and any forbidden keys before it reaches the merge.
	delete(raw, "_id")
	for k := range forbiddenKeys {
		delete(raw, k)
	}
	return map[string]any(raw), nil
}

// Merge applies the override document on top of base, returning the merged
// configuration. base is taken by value, so the caller's struct is not
// mutated. Only keys present in doc are overridden (per-field semantics). A
// nil/empty doc returns base unchanged.
//
// The returned config has applyDefaults-equivalent normalisation already done
// by the caller's original Load; Merge does not re-run defaults, so callers
// that need defaults on newly-set zero values should call Validate afterward.
func Merge(base config.Config, doc map[string]any) (config.Config, error) {
	if len(doc) == 0 {
		return base, nil
	}

	out := base // copy
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           &out,
		TagName:          "mapstructure",
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return base, fmt.Errorf("remoteconfig: build decoder: %w", err)
	}
	if err := decoder.Decode(doc); err != nil {
		return base, fmt.Errorf("remoteconfig: merge: %w", err)
	}
	return out, nil
}

// FilterChanged reports whether the filter expression lists differ between two
// configs — used by the daemon hot-reload loop to decide if the active filter
// must be rebuilt.
func FilterChanged(a, b config.Config) bool {
	return !equalStrings(a.FilterInclude, b.FilterInclude) ||
		!equalStrings(a.FilterExclude, b.FilterExclude)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
