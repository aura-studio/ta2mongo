// Package dao is the data-access layer. It exposes the Dao object, the single
// entry point the service and process layers use to reach MongoDB-backed
// persistence. dao.New owns the full data-access setup: opening the MongoDB
// connection, resolving the database, and constructing the Store.
package dao

import (
	"context"
	"sync"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/dao/ejson"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	daosql "github.com/aura-studio/tango/internal/dao/sql"
	"github.com/aura-studio/tango/internal/dao/store"
	"github.com/aura-studio/tango/internal/logging"
)

// Dao is the data-access object. It owns the MongoDB resource and the
// MongoDB-backed Store so callers depend on the dao package rather than wiring
// mongo/store directly.
type Dao struct {
	Mongo *daomongo.MongoResource
	Store *store.Store

	// sql is the lazily-built SQL Data API driver (holds the vitess parser +
	// schema cache). It is created on first (*Dao).SQL call so roles that never
	// run SQL pay neither the build cost nor a failure mode at startup.
	sqlOnce sync.Once
	sqlEng  *daosql.Driver
	sqlErr  error
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
// ejson (Mongo Data API) facade
//
// The dao package fronts the dao/ejson subpackage so external layers (api /
// gateway / cli) depend only on dao, never on dao/ejson directly. EJSONRequest /
// EJSONResponse are type aliases and DecodeEJSONRequest a thin pass-through;
// (*Dao).EJSON is the relay that runs a request against this Dao's MongoDB
// connection. This mirrors the store facade above.
// ---------------------------------------------------------------------------

// EJSONRequest is a Mongo Data API request shell. Alias for ejson.Request.
type EJSONRequest = ejson.Request

// EJSONResponse is a Mongo Data API response. Alias for ejson.Response, so its
// MarshalEJSON method is available directly on a *dao.EJSONResponse.
type EJSONResponse = ejson.Response

// Mongo Data API action identifiers (re-exported from dao/ejson).
const (
	EJSONActionFindOne   = ejson.ActionFindOne
	EJSONActionFind      = ejson.ActionFind
	EJSONActionInsertOne = ejson.ActionInsertOne
	EJSONActionUpdateOne = ejson.ActionUpdateOne
	EJSONActionDeleteOne = ejson.ActionDeleteOne
	EJSONActionAggregate = ejson.ActionAggregate

	// Schema introspection & index management.
	EJSONActionListCollections = ejson.ActionListCollections
	EJSONActionSampleFields    = ejson.ActionSampleFields
	EJSONActionListIndexes     = ejson.ActionListIndexes
	EJSONActionCreateIndexes   = ejson.ActionCreateIndexes
	EJSONActionDropIndexes     = ejson.ActionDropIndexes
	EJSONActionCreateTable     = ejson.ActionCreateTable
	EJSONActionDropTable       = ejson.ActionDropTable
)

// DecodeEJSONRequest parses an Extended-JSON Mongo Data API request body. See
// ejson.DecodeRequest.
func DecodeEJSONRequest(b []byte) (*EJSONRequest, error) {
	return ejson.DecodeRequest(b)
}

// EJSON executes a Mongo Data API request against this Dao's MongoDB connection,
// defaulting the database to the one named in the connection URI when the
// request omits it. It is the relay behind api.Engine.EJSON / gateway POST
// /ejson / cli function=ejson.
func (d *Dao) EJSON(ctx context.Context, req *EJSONRequest) (*EJSONResponse, error) {
	return ejson.Execute(ctx, d.Mongo, req)
}

// ---------------------------------------------------------------------------
// sql (SQL Data API) facade
//
// The dao package fronts the dao/sql subpackage (a thin adapter over the
// external github.com/aura-studio/mongosql dependency) so external layers depend
// only on dao. SQLResult is a type alias; (*Dao).SQL lazily builds the SQL driver
// (vitess parser + schema cache) over this Dao's MongoDB resource and relays a
// SQL string to it. Mirrors the ejson facade above.
// ---------------------------------------------------------------------------

// SQLResult is the outcome of a SQL Data API statement. Alias for sql.Result, so
// its MarshalEJSON method is available directly on a *dao.SQLResult.
type SQLResult = daosql.Result

// SQL parses and executes a SQL statement against this Dao's MongoDB connection,
// using the database named in the connection URI. The SQL driver is built once
// on first use. It is the relay behind api.Engine.SQL / gateway POST /sql / cli
// function=sql.
func (d *Dao) SQL(ctx context.Context, query string) (*SQLResult, error) {
	d.sqlOnce.Do(func() {
		d.sqlEng, d.sqlErr = daosql.New(d.Mongo)
	})
	if d.sqlErr != nil {
		return nil, d.sqlErr
	}
	return d.sqlEng.Exec(ctx, query)
}

// ---------------------------------------------------------------------------
// watch facade
//
// The dao package fronts the MongoDB change-stream API on a collection so the
// cfgsync domain can subscribe to config-document changes through dao alone,
// never reaching for the driver collection directly. As with the write-model
// constructors, the driver type (*mongo.ChangeStream) is intentionally left
// exposed — it is a MongoDB type and not ours to hide behind the façade.
// ---------------------------------------------------------------------------

// Watch opens a change stream on a collection in this Dao's default database,
// forwarding the pipeline and options to the driver as-is. The caller owns the
// returned stream and must Close it. Change streams require a replica set (plain
// MongoDB) or change streams enabled (DocumentDB); a topology that does not
// support them (e.g. standalone mongod) surfaces the driver's error here.
func (d *Dao) Watch(ctx context.Context, collection string, pipeline mongo.Pipeline, opts ...options.Lister[options.ChangeStreamOptions]) (*mongo.ChangeStream, error) {
	coll := d.Mongo.DB.Collection(collection)
	return coll.Watch(ctx, pipeline, opts...)
}
