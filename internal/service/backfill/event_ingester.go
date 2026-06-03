package backfill

import (
	"context"

	"rocket-nano/tools/tango/internal/process/ingestion"
	"rocket-nano/tools/tango/internal/process/pipeline"
)

// eventStreamBuffer is the per-worker channel buffer used when streaming rows
// from a result page into the pipeline. Sized to absorb a few flush cycles
// without blocking the HTTP read.
const eventStreamBuffer = 2048

// fetchAndIngestEventPage streams an event-table result page into the shared
// worker pipeline (parse → filter → identity → BulkWrite), one row at a time,
// so each batch reaches Mongo before the next row is read.
func (r *Runner) fetchAndIngestEventPage(ctx context.Context, taskID string, pageID int, headers []string) (int, error) {
	// The TA result-page endpoint returns one big NDJSON stream — pageCount is
	// always 1 regardless of pageSize, so a network blip mid-page would
	// otherwise lose the entire result. Stream rows directly into the worker
	// pipeline so each batch reaches Mongo before the next row is read, and
	// rely on #uuid dedup on retry.
	lineCh := make(chan string, eventStreamBuffer)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		pipeline.RunWorkers(ctx, r.cfg, r.store, r.parser, r.filter, r.logger,
			lineCh, &statsCollector{s: &r.stats},
			ingestion.WriteOptions{ForceSkipExisting: r.cfg.Backfill.ForceSkip()})
	}()

	rows := 0
	streamErr := r.client.StreamResultPage(ctx, taskID, pageID, r.cfg.Backfill.PageSize,
		func(row []interface{}) error {
			rows++
			line, err := EncodeRowAsJSONLine(headers, row)
			if err != nil {
				r.logger.WithError(err).Debug("backfill: encode row")
				return nil
			}
			select {
			case lineCh <- line:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})

	close(lineCh)
	<-workerDone
	return rows, streamErr
}
