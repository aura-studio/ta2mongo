package client

// Import-boundary audit for the public client package, in the style of
// internal/dao/ultra_importboundary_test.go (doc/ultra_test.md §5.8 DAO-6):
// the client is a thin facade over the ingestion engine and must reach tango's
// internals through internal/role/api alone. In particular it must never
// import internal/dao — the dao request/response types stay behind the
// engine's bytes-in/bytes-out faces (api.Engine EJSONBytes / SQLBytes), so the
// public surface is []byte/string only. Pure unit test — no Mongo, no network.

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/aura-studio/tango"

// forbiddenClientImport is an internal package the client package must not
// import, including all of its subpackages.
type forbiddenClientImport struct {
	pkg string // module-relative package path
}

func (f forbiddenClientImport) matches(imp string) bool {
	full := modulePath + "/" + f.pkg
	return imp == full || strings.HasPrefix(imp, full+"/")
}

// TestClient_ImportBoundaries enforces the client facade boundary: the ONLY
// tango-internal import allowed in client's production sources is
// internal/role/api —
//
//   - internal/dao (and any dao subpackage) stays behind the engine's
//     bytes-in/bytes-out query faces;
//   - the config-owning domains (dao / parser / process / cfgsync / cfgtree)
//     are reached through the api package's config aliases (api.DaoConfig,
//     api.Tree, ...), never named directly — options.go included;
//   - the sibling roles (role/cli, role/gateway, role/daemon) and the source
//     domain are not the client's to touch.
//
// Scope matches the DAO-6 precedent: the rule binds PRODUCTION sources only,
// so *_test.go files are excluded — integration tests legitimately reach into
// internals today (config_facade_test.go -> internal/cfgsync). The audit is a
// source-level import scan (go/parser ImportsOnly over every non-test .go
// file in this package directory).
func TestClient_ImportBoundaries(t *testing.T) {
	forbidden := []forbiddenClientImport{
		{pkg: "internal/dao"},
		{pkg: "internal/parser"},
		{pkg: "internal/process"},
		{pkg: "internal/cfgsync"},
		{pkg: "internal/cfgtree"},
		{pkg: "internal/source"},
		{pkg: "internal/role/cli"},
		{pkg: "internal/role/gateway"},
		{pkg: "internal/role/daemon"},
	}

	fset := token.NewFileSet()
	scanned := 0
	var violations []string

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		scanned++
		for _, spec := range f.Imports {
			imp, uerr := strconv.Unquote(spec.Path.Value)
			if uerr != nil {
				return uerr
			}
			for _, fb := range forbidden {
				if fb.matches(imp) {
					pos := fset.Position(spec.Pos())
					violations = append(violations,
						pos.String()+" imports "+imp+" (forbidden: the client goes through internal/role/api)")
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk client package: %v", err)
	}
	// Layout-drift guard: an empty scan would make this audit silently green.
	if scanned == 0 {
		t.Fatal("no non-test .go files found in the client package — directory layout changed?")
	}
	if len(violations) > 0 {
		t.Errorf("client boundary violations (%d):\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}
