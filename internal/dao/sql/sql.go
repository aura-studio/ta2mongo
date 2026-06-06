// Package sql is tango's thin adapter over github.com/aura-studio/mongosql: it
// runs SQL→MongoDB against an injected connection (a dao/mongo.MongoResource)
// and EJSON-encodes the result. The SQL parsing/translation/execution lives in
// mongosql; tango depends on it (mongosql's driver.New injection constructor)
// rather than vendoring a copy, so updates flow in via a version bump.
//
// It is a dao subpackage fronted by the dao root package (see dao.go); other
// domains reach it through dao, never importing dao/sql directly.
package sql

import (
	"context"
	"fmt"

	mongosql "github.com/aura-studio/mongosql/driver"

	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
)

// Driver runs SQL statements against an injected MongoDB connection via mongosql.
type Driver struct {
	d *mongosql.Driver
}

// New builds a SQL driver over an existing MongoDB resource. The connection is
// owned by tango's dao; mongosql is created with driver.New (injection), so it
// never dials or closes the connection.
func New(res *daomongo.MongoResource) (*Driver, error) {
	if res == nil || res.Client == nil || res.DB == nil {
		return nil, fmt.Errorf("sql: nil MongoDB resource")
	}
	d, err := mongosql.New(res.Client, res.DB)
	if err != nil {
		return nil, err
	}
	return &Driver{d: d}, nil
}

// Exec parses and executes a SQL statement, returning a tango Result (which
// adds EJSON encoding over mongosql's result shape).
func (e *Driver) Exec(ctx context.Context, query string) (*Result, error) {
	r, err := e.d.Exec(ctx, query)
	if err != nil {
		return nil, err
	}
	return fromMongosql(r), nil
}
