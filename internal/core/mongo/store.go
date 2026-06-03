package mongo

import (
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/store"
)

// NewStore constructs the Store on the given database. It is a thin assembly
// helper so callers do not reach past the runtime layer into core/store for the
// common case.
func NewStore(db *mongo.Database, cfg config.Config, logger *logrus.Logger) *store.Store {
	return store.New(db, cfg, logger)
}
