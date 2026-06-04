package config

import (
	"bytes"
	"fmt"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// LoadBytes builds a fully-resolved config Tree from in-memory config bytes — the
// gateway-compatible unified schema, YAML or JSON, detected from the content —
// layering TANGO_* env on top. It is the in-memory analogue of Load (which reads
// a file path): the same defaults < bytes < env precedence and the same
// dependency-free cfgtree.Tree that every module slices its own branch from.
//
// Empty bytes are valid and yield defaults + env. It is used by the SDK client's
// WithConfigBytes so an embedded gateway-compatible config can drive a client.
func LoadBytes(data []byte) (cfgtree.Tree, error) {
	v := newViper()
	registerAll(v.SetDefault)
	v.SetConfigType(detectConfigType(data))
	if len(data) > 0 {
		if err := v.ReadConfig(bytes.NewReader(data)); err != nil {
			return cfgtree.Tree{}, fmt.Errorf("read config bytes: %w", err)
		}
	}
	return cfgtree.New(v.AllSettings()), nil
}

// detectConfigType reports the viper config type for raw config bytes by looking
// at the first significant byte: a JSON document opens with '{' or '[', anything
// else is treated as YAML (a superset that also covers TOML-free key: value).
func detectConfigType(data []byte) string {
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{', '[':
			return "json"
		}
		break
	}
	return "yaml"
}
