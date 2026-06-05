package core

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
)

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
//
// It depends only on the dao and parser packages (not their store/talog/filter
// subpackages): prs supplies both line parsing and the reporting filter, and
// store supplies identity resolution plus the write-model constructors fronted
// by dao.
type Processor struct {
	prs   *parser.Parser
	store *dao.Store
	stats StatsCollector
}

// NewProcessor builds a Processor. prs carries both the parser and its filter
// (a parser built with a nil filter keeps every record); a nil stats collector
// is treated as a no-op.
func NewProcessor(prs *parser.Parser, st *dao.Store, stats StatsCollector) *Processor {
	if stats == nil {
		stats = NoopStats{}
	}
	return &Processor{prs: prs, store: st, stats: stats}
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
			res = Result{Kind: KindParseError, Model: dao.DeadLetterModel(line, err), Err: err}
		}
	}()

	rec, err := p.prs.ParseLine(line)
	if err != nil {
		p.stats.OnParseError()
		p.stats.OnDeadLetter()
		return Result{Kind: KindParseError, Model: dao.DeadLetterModel(line, err), Err: err}
	}
	p.stats.OnParseOK()

	// Apply the user-defined include/exclude filter. Dropped records are not
	// written anywhere — they are intentionally discarded, not dead-lettered.
	if flt := p.prs.Filter(); flt != nil && !flt.Empty() {
		keep, ferr := flt.Keep(rec.Doc)
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
		return Result{Kind: KindIdentityError, Model: dao.DeadLetterModel(line, err), Err: err}
	}

	switch rec.Category() {
	case parser.CategoryUser:
		p.stats.OnUserWrite()
		return Result{Kind: KindUser, Model: dao.UserWriteModel(rec.Type, userID, rec.Doc)}
	default: // parser.CategoryEvent
		rec.Doc["#user_id"] = userID
		p.stats.OnEventWrite()
		return Result{Kind: KindEvent, Model: dao.EventWriteModel(rec.Type, rec.UUID, rec.Doc)}
	}
}
