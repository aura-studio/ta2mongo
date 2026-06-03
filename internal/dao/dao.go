// Package dao is the data-access layer. It exposes the Dao object, the single
// entry point the service and process layers use to reach MongoDB-backed
// persistence, encapsulating the underlying store.
package dao

import (
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/dao/store"
)

// Dao is the data-access object. It wraps the MongoDB-backed Store so callers
// depend on the dao package rather than reaching into core/store directly. The
// embedded Store promotes its methods (EnsureIndexes, BulkWrite, collection
// accessors, ...) onto Dao.
type Dao struct {
	*store.Store
}

// New constructs a Dao on the given database. It projects the loaded
// configuration onto the store's own Config so the store stays decoupled from
// the top-level config package.
func New(db *mongo.Database, cfg config.Config) *Dao {
	return &Dao{Store: store.New(db, cfg.Store)}
}
