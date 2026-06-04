package client

import (
	"context"
	"time"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
)

// Option configures a Client. Options follow the functional-options idiom: pass
// any number of them to New. Each With* option maps one-to-one onto a field of
// the real tango config structs and is named after that key's full path (e.g.
// WithDaoMongoURI == dao.mongo.uri). Any option left unset keeps the ingestion
// engine's own default.
//
// Only the config subtrees the ingestion engine actually consumes are exposed:
// dao.*, parser.filter.* and process.*. The logging.*, source.* and role.*
// subtrees belong to the binary roles, not to an embedded client.
type Option func(*options)

// options holds the actual tango module config structs the ingestion engine
// consumes, so each With* sets a field of the real config structure rather than
// a parallel re-declaration: dao.Config (dao.mongo.* + dao.store.*),
// parser.Config (parser.filter.*) and process.Config (process.* +
// process.pipeline.*). New hands &dao / &proc / &parser straight to api.New.
type options struct {
	ctx context.Context

	dao    dao.Config
	parser parser.Config
	proc   process.Config
}

func defaultOptions() *options {
	o := &options{ctx: context.Background()}
	// Let each module config allocate its sub-configs and apply its own
	// defaults, so the With* setters layer on top of the engine's real
	// defaults and never dereference a nil sub-config.
	o.dao.ApplyDefaults()
	o.parser.ApplyDefaults()
	o.proc.ApplyDefaults()
	return o
}

// WithContext bounds the initial MongoDB connection established by New. It is an
// operational option, not a config key: it only governs connection setup, while
// per-call deadlines are passed to Upload and EnsureIndexes directly. Defaults
// to context.Background().
func WithContext(ctx context.Context) Option {
	return func(o *options) {
		if ctx != nil {
			o.ctx = ctx
		}
	}
}

// -------------------------------------------------------------------- dao.mongo

// WithDaoMongoURI sets dao.mongo.uri: the MongoDB connection URI (required). The
// database name is taken from the URI path (default "tango").
func WithDaoMongoURI(uri string) Option {
	return func(o *options) { o.dao.Mongo.URI = uri }
}

// WithDaoMongoConnectTimeout sets dao.mongo.connectTimeout: the bound on the
// initial MongoDB handshake (default 10s).
func WithDaoMongoConnectTimeout(d time.Duration) Option {
	return func(o *options) { o.dao.Mongo.ConnectTimeout = d }
}

// WithDaoMongoServerSelectionTimeout sets dao.mongo.serverSelectionTimeout: how
// long the driver waits for a suitable server before failing an operation
// (default 30s).
func WithDaoMongoServerSelectionTimeout(d time.Duration) Option {
	return func(o *options) { o.dao.Mongo.ServerSelectionTimeout = d }
}

// -------------------------------------------------------------------- dao.store

// WithDaoStoreMaxElapsedTime sets dao.store.maxElapsedTime: the maximum total
// retry time for a single bulk write (default 10s).
func WithDaoStoreMaxElapsedTime(d time.Duration) Option {
	return func(o *options) { o.dao.Store.MaxElapsedTime = d }
}

// --------------------------------------------------------------- parser.filter

// WithParserFilterInclude sets parser.filter.include: expr-lang expressions; a
// record is kept only when at least one matches (OR semantics). An empty list
// keeps every record. Repeated calls append.
func WithParserFilterInclude(exprs ...string) Option {
	return func(o *options) { o.parser.Filter.Include = append(o.parser.Filter.Include, exprs...) }
}

// WithParserFilterExclude sets parser.filter.exclude: expr-lang expressions; a
// record is dropped when any matches. Applied after include. Repeated calls
// append.
func WithParserFilterExclude(exprs ...string) Option {
	return func(o *options) { o.parser.Filter.Exclude = append(o.parser.Filter.Exclude, exprs...) }
}

// ------------------------------------------------------------------- process.*

// WithProcessMode sets process.mode: the upload strategy "single", "batch"
// (default) or "pipeline". An empty string keeps the default.
func WithProcessMode(mode string) Option {
	return func(o *options) { o.proc.Mode = mode }
}

// WithProcessBatchSize sets process.batchSize: the bulk-write flush size for the
// single/batch strategies (default 1000).
func WithProcessBatchSize(n int) Option {
	return func(o *options) { o.proc.BatchSize = n }
}

// ----------------------------------------------------------- process.pipeline.*
// These tune the asynchronous "pipeline" strategy; they only take effect
// together with WithProcessMode("pipeline").

// WithProcessPipelineBatchSize sets process.pipeline.batchSize: target records
// per bulk-write flush (default 1000).
func WithProcessPipelineBatchSize(n int) Option {
	return func(o *options) { o.proc.Pipeline.BatchSize = n }
}

// WithProcessPipelineBatchSizeMin sets process.pipeline.batchSizeMin: the
// adaptive lower bound for batch sizing. Zero auto-derives BatchSize/4 (min 1).
func WithProcessPipelineBatchSizeMin(n int) Option {
	return func(o *options) { o.proc.Pipeline.BatchSizeMin = n }
}

// WithProcessPipelineBatchSizeMax sets process.pipeline.batchSizeMax: the
// adaptive upper bound for batch sizing. Zero auto-derives BatchSize*2.
func WithProcessPipelineBatchSizeMax(n int) Option {
	return func(o *options) { o.proc.Pipeline.BatchSizeMax = n }
}

// WithProcessPipelineBatchWorkers sets process.pipeline.batchWorkers: the number
// of parallel write workers (default 2).
func WithProcessPipelineBatchWorkers(n int) Option {
	return func(o *options) { o.proc.Pipeline.BatchWorkers = n }
}

// WithProcessPipelineFlushInterval sets process.pipeline.flushInterval: how often
// workers flush partial batches (default 1s).
func WithProcessPipelineFlushInterval(d time.Duration) Option {
	return func(o *options) { o.proc.Pipeline.FlushInterval = d }
}

// WithProcessPipelineChannelBuffer sets process.pipeline.channelBuffer: the
// per-worker line-channel buffer. Zero derives BatchSize*2.
func WithProcessPipelineChannelBuffer(n int) Option {
	return func(o *options) { o.proc.Pipeline.ChannelBuffer = n }
}

// WithProcessPipelineDeadLetterCap sets process.pipeline.deadLetterCap: the
// per-worker dead-letter batch capacity (default 128).
func WithProcessPipelineDeadLetterCap(n int) Option {
	return func(o *options) { o.proc.Pipeline.DeadLetterCap = n }
}
