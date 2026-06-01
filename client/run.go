package client

import (
	"context"
	"fmt"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/service/backfill"
)

// BackfillResult summarises a backfill execution (function #3).
type BackfillResult struct {
	UserWrites  int64 `json:"userWrites"`
	EventWrites int64 `json:"eventWrites"`
	Filtered    int64 `json:"filtered"`
	DaysFailed  int64 `json:"daysFailed"`
}

// RunBackfill executes a checkpointed historical backfill. rt is a runtime
// config (build it from config.ClientConfig.BackfillRuntime()); the client's
// own connection URI is enforced so the backfill targets the same database.
func (c *Client) RunBackfill(ctx context.Context, rt config.Config) (BackfillResult, error) {
	rt.Mongo.URI = c.opts.URI
	rt.Mode = config.ModeBackfill
	if err := rt.Validate(); err != nil {
		return BackfillResult{}, fmt.Errorf("client: backfill config invalid: %w", err)
	}
	r, err := backfill.New(ctx, rt, c.logger)
	if err != nil {
		return BackfillResult{}, err
	}
	defer r.Shutdown()
	if err := r.EnsureIndexes(ctx); err != nil {
		return BackfillResult{}, err
	}
	if err := r.Run(ctx); err != nil {
		return BackfillResult{}, err
	}
	s := r.Stats()
	return BackfillResult{
		UserWrites:  s.UserWrites.Load(),
		EventWrites: s.EventWrites.Load(),
		Filtered:    s.Filtered.Load(),
		DaysFailed:  s.DaysFailed.Load(),
	}, nil
}

// ExecuteSQL runs an ad-hoc SQL statement against the TA OpenAPI and imports
// the rows (function #4). rt is a runtime config (build it from
// config.ClientConfig.SQLRuntime()); table selection comes from
// rt.BackfillFilter.Table.
func (c *Client) ExecuteSQL(ctx context.Context, rt config.Config, sql string) (int64, error) {
	if sql == "" {
		return 0, fmt.Errorf("client: ExecuteSQL: sql is required")
	}
	rt.Mongo.URI = c.opts.URI
	rt.Mode = config.ModeBackfill
	r, err := backfill.NewExecutor(ctx, rt, c.logger)
	if err != nil {
		return 0, err
	}
	defer r.Shutdown()
	if err := r.EnsureIndexes(ctx); err != nil {
		return 0, err
	}
	return r.ExecuteSQL(ctx, sql)
}
