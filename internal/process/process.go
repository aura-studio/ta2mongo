// Package process is the unified entry point to the three ingestion modes:
//
//   - single   — per-line processing (internal/process/single)
//   - batch    — synchronous, batched ingestion (internal/process/batch)
//   - pipeline — asynchronous streaming workers (internal/process/pipeline)
//
// External layers (the engine modes and the client SDK) depend on this package
// only; the single/batch/pipeline sub-packages are managed here and are not
// meant to be imported directly from outside process.
package process

import (
	"context"

	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process/batch"
	"rocket-nano/tools/tango/internal/process/pipeline"
	"rocket-nano/tools/tango/internal/process/single"
)

// Ingester is the synchronous single/batch ingestion handle (Ingest /
// IngestBatch). It is produced by NewIngester.
type Ingester = batch.Ingester

// Counters accumulates per-line processing statistics for the pipeline mode.
type Counters = single.Counters

// Snapshot is an immutable read of a Counters at a point in time.
type Snapshot = single.Snapshot

// WriteOptions tunes write-side behaviour shared by all three modes.
type WriteOptions = single.WriteOptions

// NewIngester connects to MongoDB and returns a synchronous Ingester. The caller
// must Close it.
func NewIngester(ctx context.Context, daoCfg *dao.Config, parserCfg *parser.Config) (*Ingester, error) {
	return batch.New(ctx, daoCfg, parserCfg)
}

// RunPipeline runs the asynchronous streaming pipeline, reading lines from
// lineCh and writing them through d, parsing/filtering via p. It blocks until
// all workers finish (ctx cancelled or lineCh closed). A nil stats is treated
// as a no-op collector.
func RunPipeline(ctx context.Context, cfg *Config, d *dao.Dao, p *parser.Parser,
	lineCh <-chan string, stats *Counters, opts WriteOptions,
) {
	pipeline.RunWorkers(ctx, cfg.Pipeline, d.Store, p.Parser, p.Filter(), lineCh, stats, opts)
}
