// Package dao is the data-access layer. It exposes the Dao object, the single
// entry point the service and process layers use to reach MongoDB-backed
// persistence. dao.New owns the full data-access setup: opening the MongoDB
// connection, resolving the database, and constructing the Store.
package dao

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/aura-studio/tango/internal/dao/data"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/dao/store"
	"github.com/aura-studio/tango/internal/logging"
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
	logging.WithField("db", res.DB.Name()).Info("dao: connected to MongoDB")
	return &Dao{
		Mongo: res,
		Store: store.New(res.DB, cfg.Store),
	}, nil
}

// ---------------------------------------------------------------------------
// store facade
//
// The dao package fronts the dao/store subpackage so external layers (notably
// the process uploaders) depend only on dao, never on dao/store directly. Store
// is re-exported as a type alias and the write-model constructors as thin
// pass-throughs. The mongo.WriteModel return type is a MongoDB driver type and
// is intentionally left exposed — it is not ours to hide behind the dao facade.
// ---------------------------------------------------------------------------

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

// DeadLetterModel builds an insert model for a line that failed to parse or
// resolve identity. See store.DeadLetterModel.
func DeadLetterModel(line string, parseErr error) mongo.WriteModel {
	return store.DeadLetterModel(line, parseErr)
}

// ---------------------------------------------------------------------------
// data (Mongo Data API) facade
//
// The dao package fronts the dao/data subpackage so external layers (api /
// gateway / cli) depend only on dao, never on dao/data directly. DataRequest /
// DataResponse are type aliases and DecodeDataRequest a thin pass-through;
// (*Dao).Data is the relay that runs a request against this Dao's MongoDB
// connection. This mirrors the store facade above.
// ---------------------------------------------------------------------------

// DataRequest is a Mongo Data API request shell. Alias for data.Request.
type DataRequest = data.Request

// DataResponse is a Mongo Data API response. Alias for data.Response, so its
// MarshalEJSON method is available directly on a *dao.DataResponse.
type DataResponse = data.Response

// Mongo Data API action identifiers (re-exported from dao/data).
const (
	DataActionFindOne   = data.ActionFindOne
	DataActionFind      = data.ActionFind
	DataActionInsertOne = data.ActionInsertOne
	DataActionUpdateOne = data.ActionUpdateOne
	DataActionDeleteOne = data.ActionDeleteOne
	DataActionAggregate = data.ActionAggregate
)

// DecodeDataRequest parses an Extended-JSON Mongo Data API request body. See
// data.DecodeRequest.
func DecodeDataRequest(b []byte) (*DataRequest, error) {
	return data.DecodeRequest(b)
}

// Data executes a Mongo Data API request against this Dao's MongoDB connection,
// defaulting the database to the one named in the connection URI when the
// request omits it. It is the relay behind api.Engine.Data / gateway POST /data /
// cli function=data.
func (d *Dao) Data(ctx context.Context, req *DataRequest) (*DataResponse, error) {
	return data.Execute(ctx, d.Mongo, req)
}
