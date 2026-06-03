// Package dao is the data-access layer. It exposes the Dao object, the single
// entry point the service and process layers use to reach MongoDB-backed
// persistence, encapsulating the underlying mongo resource and store.
package dao

import (
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

// New constructs a Dao on the resolved Mongo resource.
func New(res *daomongo.MongoResource, cfg *Config) *Dao {
	return &Dao{Mongo: res, Store: store.New(res.DB, cfg.Store)}
}
