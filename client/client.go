// Package client is the public, redis-go-style client for tango's log-ingestion
// engine. Construct a Client with New plus functional With* options, then feed
// it ThinkingData log lines via Upload — or bulk import already-on-disk log
// files via UploadFile (glob patterns, one-shot, no checkpoint).
//
// It is a thin, stable facade over the internal ingestion engine
// (internal/role/api): callers depend only on this interface and never on
// tango's internal packages. The same engine backs the daemon/gateway/cli roles,
// so an Upload here uses the identical parse → filter → identity-resolve → write
// path.
//
// Usage:
//
//	c, err := client.New(
//		client.WithDaoMongoURI("mongodb://localhost:27017/tango"),
//		client.WithProcessMode("pipeline"),
//	)
//	if err != nil {
//		// ...
//	}
//	defer c.Close()
//	res, err := c.Upload(ctx, line1, line2)
//
// Instead of (or as a base for) individual With* options, a whole
// gateway-compatible config file can drive the client via WithConfigFile or
// WithConfigBytes — the same unified schema the tango binary's --config accepts,
// of which only dao.*, parser.filter.* and process.* are consumed:
//
//	c, err := client.New(client.WithConfigBytes(embeddedConfigYAML))
//
// Beyond log ingestion the client also exposes the cfgsync central-config faces
// (the same cores behind gateway POST/GET /config): PublishConfig/AppendConfig
// validate and write the runtime config document, FetchConfig reads it back.
// Likewise it exposes the Data API query faces (the same cores behind gateway
// POST /ejson and /sql): EJSON runs a Mongo Data API request shell and SQL runs
// a SQL statement, both returning relaxed Extended JSON.
package client

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/role/api"
)

// Result summarises a single Upload run.
type Result struct {
	Lines       int64 `json:"lines"`
	UserWrites  int64 `json:"userWrites"`
	EventWrites int64 `json:"eventWrites"`
	DeadLetters int64 `json:"deadLetters"`
	Filtered    int64 `json:"filtered"`
}

// Client is tango's ingestion client. It is safe for concurrent use; the
// underlying MongoDB driver manages the connection pool. Always Close it.
type Client interface {
	// Upload ingests the given TA log lines with the configured strategy and
	// returns per-run statistics. Lines that fail to parse or resolve identity
	// are routed to dead_letter (counted in Result) rather than erroring; a
	// non-nil error indicates a bulk-write failure or an unknown mode.
	Upload(ctx context.Context, lines ...string) (Result, error)
	// UploadFile bulk imports the already-on-disk TA log files matching the
	// given glob patterns (the tailer's pattern syntax, including ** and
	// cross-platform paths) once — every file is read start to EOF through the
	// configured strategy, then the run ends. At least one pattern is
	// required; patterns that match no files yield an empty Result. There is
	// no checkpoint: re-running re-imports everything and converges through
	// the idempotent write models (events upsert by uuid, user fields are
	// _ts-guarded). The per-line error contract matches Upload. The line cap
	// defaults to 10 MB; tune it with WithSourceUploadFileMaxLineBytes.
	UploadFile(ctx context.Context, patterns ...string) (Result, error)
	// EnsureIndexes creates all required MongoDB indexes (idempotent).
	EnsureIndexes(ctx context.Context) error
	// PublishConfig validates and publishes a runtime config document to the
	// central cfgsync collection, returning the new monotonic version. doc
	// carries the config content (e.g. {"filter": {"include": [...],
	// "exclude": [...]}}); cfgsync owns _id and version. A document carrying a
	// subtree off the allowlist, or a filter that does not compile, is rejected
	// before any write. The daemon/gateway watchers pick the new version up.
	PublishConfig(ctx context.Context, doc map[string]any) (int64, error)
	// AppendConfig is the merge flavour of PublishConfig: the published filter
	// rules are unioned into the stored ones (an omitted include/exclude side
	// keeps its stored value) under an optimistic version guard, so concurrent
	// appends never lose each other's rules. Same validation gates as
	// PublishConfig, run on the merged document.
	AppendConfig(ctx context.Context, doc map[string]any) (int64, error)
	// FetchConfig returns the current central config document (including its
	// monotonic version), or (nil, nil) when nothing has been published yet.
	// The document carries only plain Go values — map[string]any, []any and
	// scalars — never driver types, so callers stay decoupled from BSON.
	FetchConfig(ctx context.Context) (map[string]any, error)
	// EJSON executes a Mongo Data API request against the connected MongoDB and
	// returns the response as relaxed Extended JSON. request is the same
	// Extended-JSON request shell gateway POST /ejson accepts ({"action":
	// "find", "collection": ..., "filter": ..., ...}); the database defaults to
	// the one named in the connection URI when the shell omits it. The query
	// faces are independent of the upload pipeline — process/parser config is
	// unused.
	EJSON(ctx context.Context, request []byte) ([]byte, error)
	// SQL parses and executes a SQL statement against the connected MongoDB
	// (SQL Data API, same core as gateway POST /sql) and returns the result as
	// relaxed Extended JSON: a SELECT carries its rows, a write its affected
	// counts. Like EJSON it is independent of the upload pipeline.
	SQL(ctx context.Context, query string) ([]byte, error)
	// Close disconnects from MongoDB and releases all resources.
	Close() error
}

type client struct {
	engine *api.Engine
	// uploadfile carries the source.uploadfile.* tuning (maxLineBytes) the
	// UploadFile face combines with its call-time patterns; the engine
	// constructs the source from it (the client never touches the source
	// domain).
	uploadfile api.UploadFileConfig
}

// New connects to MongoDB and builds a Client from the given options.
// WithDaoMongoURI is required; without it New returns an error. The caller must
// Close the returned Client.
//
// The With* options populate the real tango module config structs held by
// options, so New hands their addresses straight to api.New rather than copying
// fields onto parallel structs. An all-empty filter config builds the no-op
// "keep everything" filter, matching the engine's default.
func New(opts ...Option) (Client, error) {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}
	if o.err != nil {
		return nil, o.err
	}

	engine, err := api.New(o.ctx, &o.dao, &o.proc, &o.parser, &o.cfgsync)
	if err != nil {
		return nil, err
	}
	return &client{engine: engine, uploadfile: o.uploadfile}, nil
}

func (c *client) Upload(ctx context.Context, lines ...string) (Result, error) {
	res, err := c.engine.Upload(ctx, lines)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Lines:       res.Lines,
		UserWrites:  res.UserWrites,
		EventWrites: res.EventWrites,
		DeadLetters: res.DeadLetters,
		Filtered:    res.Filtered,
	}, nil
}

func (c *client) UploadFile(ctx context.Context, patterns ...string) (Result, error) {
	cfg := c.uploadfile // copy: the call-time patterns never mutate the client
	cfg.LogPattern = patterns
	res, err := c.engine.UploadFile(ctx, &cfg)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Lines:       res.Lines,
		UserWrites:  res.UserWrites,
		EventWrites: res.EventWrites,
		DeadLetters: res.DeadLetters,
		Filtered:    res.Filtered,
	}, nil
}

func (c *client) EnsureIndexes(ctx context.Context) error { return c.engine.EnsureIndexes(ctx) }

func (c *client) PublishConfig(ctx context.Context, doc map[string]any) (int64, error) {
	return c.engine.PublishConfig(ctx, bson.M(doc))
}

func (c *client) AppendConfig(ctx context.Context, doc map[string]any) (int64, error) {
	return c.engine.AppendConfig(ctx, bson.M(doc))
}

func (c *client) FetchConfig(ctx context.Context) (map[string]any, error) {
	doc, err := c.engine.FetchConfig(ctx)
	if err != nil || doc == nil {
		return nil, err
	}
	norm := make(map[string]any, len(doc))
	for k, v := range doc {
		norm[k] = normalizeValue(v)
	}
	return norm, nil
}

// normalizeValue recursively converts the BSON container types the driver
// decodes into (bson.D / bson.M / bson.A) to plain Go containers, so
// FetchConfig's contract — no driver types in the returned document — holds at
// every nesting level.
func normalizeValue(v any) any {
	switch t := v.(type) {
	case bson.D:
		m := make(map[string]any, len(t))
		for _, e := range t {
			m[e.Key] = normalizeValue(e.Value)
		}
		return m
	case bson.M:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = normalizeValue(val)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, val := range t {
			m[k] = normalizeValue(val)
		}
		return m
	case bson.A:
		s := make([]any, len(t))
		for i, val := range t {
			s[i] = normalizeValue(val)
		}
		return s
	case []any:
		s := make([]any, len(t))
		for i, val := range t {
			s[i] = normalizeValue(val)
		}
		return s
	default:
		return v
	}
}

// EJSON and SQL keep the public surface at []byte/string so callers never see
// internal request/response types: they forward to the engine's
// bytes-in/bytes-out faces (EJSONBytes / SQLBytes), where the dao-typed
// decode/encode lives — the client package never imports internal/dao.

func (c *client) EJSON(ctx context.Context, request []byte) ([]byte, error) {
	return c.engine.EJSONBytes(ctx, request)
}

func (c *client) SQL(ctx context.Context, query string) ([]byte, error) {
	return c.engine.SQLBytes(ctx, query)
}

func (c *client) Close() error { return c.engine.Close() }
