package dao

// Ultra-test coverage for doc/ultra_test.md §5.8 DAO-6 (domain import
// boundaries) plus the dao-side half of §5.7 SQL-2 ((*Dao).SQL sync.Once error
// caching). Pure unit tests — no Mongo, no network.

import (
	"context"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/aura-studio/tango"

// forbiddenImport is a package other domains may only reach through its root
// facade. When subOnly is true, only subpackages are forbidden and the package
// itself is allowed (role may use the source facade, just not source/tailer &co).
type forbiddenImport struct {
	pkg     string // module-relative package path
	subOnly bool
}

func (f forbiddenImport) matches(imp string) bool {
	full := modulePath + "/" + f.pkg
	if f.subOnly {
		return strings.HasPrefix(imp, full+"/")
	}
	return imp == full || strings.HasPrefix(imp, full+"/")
}

// TestDAO6_ImportBoundaries enforces doc/ultra_test.md §5.8 DAO-6: domains
// talk to each other only through root-package facades —
//
//   - process/* must not import dao/store (it goes through the dao facade:
//     dao.UserWriteModel / dao.EventWriteModel / dao.DeadLetterModel in
//     dao.go) nor parser/talog or parser/filter (it goes through parser);
//   - role/* must not import source subpackages (source/tailer, source/...;
//     the source root facade is allowed) nor parser/filter;
//   - cfgsync must not import dao/ejson nor parser/filter.
//
// Scope matches reality: the rule binds PRODUCTION sources only, so *_test.go
// files are excluded — white-box/integration tests legitimately reach into
// subpackages today (process/batch/ingest_integration_test.go -> dao/store,
// process/core/processor_test.go -> parser/filter,
// role/daemon/watchdog_test.go -> source/tailer).
//
// The audit is a source-level import scan (go/parser ImportsOnly over every
// non-test .go file), the dependency-free equivalent of the `go list -deps`
// check the spec suggests.
func TestDAO6_ImportBoundaries(t *testing.T) {
	rules := []struct {
		dir       string // relative to this package dir (internal/dao)
		forbidden []forbiddenImport
	}{
		{
			dir: "../process",
			forbidden: []forbiddenImport{
				{pkg: "internal/dao/store"},
				{pkg: "internal/parser/talog"},
				{pkg: "internal/parser/filter"},
			},
		},
		{
			dir: "../role",
			forbidden: []forbiddenImport{
				{pkg: "internal/source", subOnly: true},
				{pkg: "internal/parser/filter"},
			},
		},
		{
			dir: "../cfgsync",
			forbidden: []forbiddenImport{
				{pkg: "internal/dao/ejson"},
				{pkg: "internal/parser/filter"},
			},
		},
	}

	for _, rule := range rules {
		t.Run(filepath.Base(rule.dir), func(t *testing.T) {
			fset := token.NewFileSet()
			scanned := 0
			var violations []string

			err := filepath.WalkDir(rule.dir, func(path string, d fs.DirEntry, err error) error {
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
					for _, fb := range rule.forbidden {
						if fb.matches(imp) {
							pos := fset.Position(spec.Pos())
							violations = append(violations,
								pos.String()+" imports "+imp+" (forbidden: use the "+facadeFor(fb.pkg)+" facade)")
						}
					}
				}
				return nil
			})
			if err != nil {
				t.Fatalf("walk %s: %v", rule.dir, err)
			}
			// Layout-drift guard: an empty scan would make this audit silently green.
			if scanned == 0 {
				t.Fatalf("no non-test .go files found under %s — directory layout changed?", rule.dir)
			}
			if len(violations) > 0 {
				t.Errorf("DAO-6 boundary violations in %s (%d):\n  %s",
					rule.dir, len(violations), strings.Join(violations, "\n  "))
			}
		})
	}
}

// facadeFor names the root facade that fronts a forbidden subpackage.
func facadeFor(pkg string) string {
	switch {
	case strings.HasPrefix(pkg, "internal/dao"):
		return "dao"
	case strings.HasPrefix(pkg, "internal/parser"):
		return "parser"
	case strings.HasPrefix(pkg, "internal/source"):
		return "source"
	}
	return "root"
}

// TestSQL2_DaoSQL_InitErrorCachedByOnce covers the dao-side half of
// doc/ultra_test.md §5.7 SQL-2: (*Dao).SQL builds the SQL driver lazily
// under d.sqlOnce (dao.go); when the construction fails, the error is stored
// in d.sqlErr ONCE and every subsequent call returns the very same error
// value. With a nil Mongo resource, daosql.New(nil) fails with
// "sql: nil MongoDB resource" — pointer-identity of the errors across calls
// proves sqlOnce ran the constructor exactly once and cached the result.
func TestSQL2_DaoSQL_InitErrorCachedByOnce(t *testing.T) {
	ctx := context.Background()
	d := &Dao{} // Mongo == nil -> daosql.New(nil) fails inside (*Dao).SQL

	res1, err1 := d.SQL(ctx, "SELECT 1")
	if err1 == nil {
		t.Fatal("first SQL call on a Dao without Mongo: want error, got nil")
	}
	if res1 != nil {
		t.Fatalf("first SQL call: want nil result with error, got %+v", res1)
	}
	if got := err1.Error(); got != "sql: nil MongoDB resource" {
		t.Fatalf("init error text: want %q, got %q", "sql: nil MongoDB resource", got)
	}

	res2, err2 := d.SQL(ctx, "SELECT 2")
	if res2 != nil {
		t.Fatalf("second SQL call: want nil result, got %+v", res2)
	}
	// Identity, not just equality: the SAME cached error instance comes back,
	// proving sync.Once did not re-run daosql.New.
	if err1 != err2 {
		t.Fatalf("init error not cached by sqlOnce: first %p (%v) vs second %p (%v)",
			err1, err1, err2, err2)
	}
}
