package dao

import (
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/internal/dao/store"
)

// This file fronts the dao/store subpackage so external layers (notably the
// process uploaders) depend only on the dao package, never on dao/store
// directly. Store is re-exported as a type alias and the write-model
// constructors as thin pass-throughs. The mongo.WriteModel return type is a
// MongoDB driver type and is intentionally left exposed — it is not ours to
// hide behind the dao facade.

// Store is the MongoDB-backed persistence handle. It is an alias for
// store.Store, so dao.Store and the *store.Store reachable via Dao.Store are
// the same type.
type Store = store.Store

// UserWriteModel builds the MongoDB write model for a user_* operation, keyed by
// the resolved #user_id. See store.UserWriteModel.
func UserWriteModel(typ string, userID int64, doc map[string]any) mongo.WriteModel {
	return store.UserWriteModel(typ, userID, doc)
}

// EventWriteModel builds the MongoDB write model for a track* operation, keyed
// by #uuid. See store.EventWriteModel.
func EventWriteModel(typ, uuid string, doc map[string]any) mongo.WriteModel {
	return store.EventWriteModel(typ, uuid, doc)
}

// EventWriteModelSkipExisting builds a $setOnInsert event write model keyed by
// #uuid, never modifying an existing document. See
// store.EventWriteModelSkipExisting.
func EventWriteModelSkipExisting(uuid string, doc map[string]any) mongo.WriteModel {
	return store.EventWriteModelSkipExisting(uuid, doc)
}

// DeadLetterModel builds an insert model for a line that failed to parse or
// resolve identity. See store.DeadLetterModel.
func DeadLetterModel(line string, parseErr error) mongo.WriteModel {
	return store.DeadLetterModel(line, parseErr)
}
