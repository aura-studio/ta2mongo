// Package translator parses SQL with vitess and dispatches to the
// statement-specific translator subpackages. It is the entry point for
// turning a SQL string into an executable stmt.Statement.
package translator

import (
	"fmt"

	"vitess.io/vitess/go/vt/sqlparser"

	"github.com/aura-studio/tango/internal/dao/sql/translator/internal/sel"
	"github.com/aura-studio/tango/internal/dao/sql/translator/internal/write"
	"github.com/aura-studio/tango/internal/dao/sql/translator/stmt"
)

// Statement re-exports stmt.Statement so callers can keep importing only
// this package.
type Statement = stmt.Statement

// Translator parses SQL and produces an executable Statement.
type Translator struct {
	parser *sqlparser.Parser
}

// New constructs a translator using the default vitess parser.
func New() (*Translator, error) {
	p, err := sqlparser.New(sqlparser.Options{})
	if err != nil {
		return nil, err
	}
	return &Translator{parser: p}, nil
}

// Translate parses sql and returns the corresponding Statement.
func (t *Translator) Translate(sql string) (Statement, error) {
	parsed, err := t.parser.Parse(sql)
	if err != nil {
		return nil, fmt.Errorf("parse SQL: %w", err)
	}
	switch s := parsed.(type) {
	case *sqlparser.Select:
		return sel.Translate(s)
	case *sqlparser.Insert:
		return write.Insert(s)
	case *sqlparser.Update:
		return write.Update(s)
	case *sqlparser.Delete:
		return write.Delete(s)
	}
	return nil, fmt.Errorf("unsupported statement type: %T", parsed)
}
