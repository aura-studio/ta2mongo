package single

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/mongo"

	daostore "rocket-nano/tools/tango/internal/dao/store"
	"rocket-nano/tools/tango/internal/logging"
	"rocket-nano/tools/tango/internal/parser/filter"
	"rocket-nano/tools/tango/internal/parser/talog"
)

// WriteOptions tunes write-side behaviour for callers that need to deviate from
// the default per-#type semantics (notably backfill).
type WriteOptions struct {
	// ForceSkipExisting routes every event write through $setOnInsert keyed by
	// #uuid, regardless of the record's #type. Existing documents are never
	// modified — duplicates become no-ops. Recommended for backfill.
	ForceSkipExisting bool
}

// Kind classifies the outcome of processing one line.
type Kind int

const (
	// KindEvent: a track* record producing an event write model.
	KindEvent Kind = iota
	// KindUser: a user_* record producing a user write model.
	KindUser
	// KindFiltered: parsed but dropped by the include/exclude filter (no model,
	// not written anywhere — intentional discard).
	KindFiltered
	// KindParseError: the line failed to parse; Model is a dead-letter model.
	KindParseError
	// KindIdentityError: identity resolution failed; Model is a dead-letter model.
	KindIdentityError
)

// Result is the classified outcome of Process for one line. The caller enqueues
// or writes Model according to Kind (and ignores it for KindFiltered). Err
// carries the underlying parse / identity error for the error kinds.
type Result struct {
	Kind  Kind
	Model mongo.WriteModel
	Err   error
}

// Processor applies the shared parse → filter → identity → write-model rules to
// a single line, recording uniform per-line stats. It never writes to MongoDB
// and never logs — the caller owns batching/writing and any caller-specific
// logging, driven by the returned Result.
type Processor struct {
	parser *talog.Parser
	filter *filter.Holder
	store  *daostore.Store
	stats  StatsCollector
	opts   WriteOptions
}

// NewProcessor builds a Processor. A nil filter holder is treated as "keep
// everything"; a nil stats collector is treated as a no-op.
func NewProcessor(parser *talog.Parser, flt *filter.Holder, st *daostore.Store, stats StatsCollector, opts WriteOptions) *Processor {
	if stats == nil {
		stats = NoopStats{}
	}
	return &Processor{parser: parser, filter: flt, store: st, stats: stats, opts: opts}
}

// Process runs one line through the ingestion rules and returns its
// classification. Stats recorded: OnLine always; then OnParseError+OnDeadLetter
// (parse failure), OnParseOK, OnFilterError (eval error), OnFiltered (dropped),
// OnIdentityError+OnDeadLetter (identity failure), or OnUserWrite/OnEventWrite.
func (p *Processor) Process(ctx context.Context, line string) (res Result) {
	p.stats.OnLine()

	// A malformed line must never crash the worker/process: recover any panic
	// from parsing, filtering, identity resolution, or write-model building and
	// route the offending line to dead_letter instead.
	defer func() {
		if r := recover(); r != nil {
			p.stats.OnParseError()
			p.stats.OnDeadLetter()
			err := fmt.Errorf("panic processing line: %v", r)
			logging.WithField("panic", r).Warn("process: recovered panic on line, sent to dead_letter")
			res = Result{Kind: KindParseError, Model: daostore.DeadLetterModel(line, err), Err: err}
		}
	}()

	rec, err := p.parser.ParseLine(line)
	if err != nil {
		p.stats.OnParseError()
		p.stats.OnDeadLetter()
		return Result{Kind: KindParseError, Model: daostore.DeadLetterModel(line, err), Err: err}
	}
	p.stats.OnParseOK()

	// Apply the user-defined include/exclude filter. Dropped records are not
	// written anywhere — they are intentionally discarded, not dead-lettered.
	if p.filter != nil && !p.filter.Empty() {
		keep, ferr := p.filter.Keep(rec.Doc)
		if ferr != nil {
			p.stats.OnFilterError()
		}
		if !keep {
			p.stats.OnFiltered()
			return Result{Kind: KindFiltered}
		}
	}

	userID, err := p.store.Identity().Resolve(ctx, rec.AccountID, rec.DistinctID)
	if err != nil {
		p.stats.OnIdentityError()
		p.stats.OnDeadLetter()
		return Result{Kind: KindIdentityError, Model: daostore.DeadLetterModel(line, err), Err: err}
	}

	switch rec.Category() {
	case talog.CategoryUser:
		p.stats.OnUserWrite()
		return Result{Kind: KindUser, Model: daostore.UserWriteModel(rec.Type, userID, rec.Doc)}
	default: // talog.CategoryEvent
		rec.Doc["#user_id"] = userID
		p.stats.OnEventWrite()
		if p.opts.ForceSkipExisting {
			return Result{Kind: KindEvent, Model: daostore.EventWriteModelSkipExisting(rec.UUID, rec.Doc)}
		}
		return Result{Kind: KindEvent, Model: daostore.EventWriteModel(rec.Type, rec.UUID, rec.Doc)}
	}
}
