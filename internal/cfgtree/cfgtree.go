// Package cfgtree is the dependency-free configuration carrier. A Tree wraps a
// fully-resolved settings map (defaults + file + TANGO_* env + flags already
// baked in by config.Load) and lets any module slice its own branch by key and
// decode it into the module's own Config struct.
//
// It depends only on mapstructure — never on viper or on tango's own packages —
// so the modules that decode their config (and the engine/SDK that embed them)
// do not pull in the config framework. The config package owns viper and hands
// cfgtree the already-materialised settings.
//
// Slicing walks the same shared map by key (Sub accumulates a path); it never
// re-roots into a child viper, so every override the loader resolved is
// preserved. The map keys are whatever config.Load produced (viper lowercases
// them); leaf matching against struct mapstructure tags is case-insensitive.
package cfgtree

import "github.com/go-viper/mapstructure/v2"

// Tree is a handle to a configuration subtree: the shared resolved settings map
// plus the path of the branch it is rooted at (empty == the whole tree). The
// zero Tree is usable and decodes nothing (callers then get pure defaults).
type Tree struct {
	settings map[string]any
	path     []string
}

// New returns a Tree rooted at the whole of settings (a nested, fully-resolved
// map, e.g. from viper's AllSettings).
func New(settings map[string]any) Tree { return Tree{settings: settings} }

// Sub narrows to a child branch, accumulating the key path (e.g.
// t.Sub("role").Sub("gateway") targets role → gateway). It shares the same
// underlying map, so all overrides are preserved.
func (t Tree) Sub(key string) Tree {
	if key == "" {
		return t
	}
	p := make([]string, len(t.path)+1)
	copy(p, t.path)
	p[len(t.path)] = key
	return Tree{settings: t.settings, path: p}
}

// Into decodes the current branch into dst (a module's own *Config), applying
// the shared duration / comma-slice decode hooks and weak typing (so TANGO_* env
// strings coerce into typed fields). A missing branch or zero Tree leaves dst
// untouched, so the caller's ApplyDefaults still yields a usable config.
func (t Tree) Into(dst any) error {
	node := t.resolve()
	if node == nil {
		return nil
	}
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           dst,
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return err
	}
	return dec.Decode(node)
}

// resolve walks the settings map along the accumulated path and returns the node
// found there, or nil if any segment is absent or not a map.
func (t Tree) resolve() any {
	var cur any = t.settings
	for _, k := range t.path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[k]
	}
	return cur
}
