// Package dao is the data-access layer. It exposes the Dao object, the single
// entry point the service and process layers use to reach MongoDB-backed
// persistence. dao.New owns the full data-access setup: opening the MongoDB
// connection, resolving the database, and constructing the Store.
package dao

import (
	"context"

	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/dao/store"
)

// Dao is the data-access object. It owns the MongoDB resource and the
// MongoDB-backed Store so callers depend on the dao package rather than wiring
// mongo/store directly.
type Dao struct {
	Mongo *daomongo.MongoResource
	Store *store.Store
}

// New opens a MongoDB connection from cfg and constructs a Dao. The caller
// must Close it. A nil or partially-populated cfg is defaulted so callers need
// not pre-fill the mongo/store sub-configs.
func New(ctx context.Context, cfg *Config) (*Dao, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.ApplyDefaults()

	res, err := daomongo.ConnectMongo(ctx, cfg.Mongo)
	if err != nil {
		return nil, err
	}
	return &Dao{
		Mongo: res,
		Store: store.New(res.DB, cfg.Store),
	}, nil
}
