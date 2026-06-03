package worker

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/taskqueue"
	"rocket-nano/tools/tango/internal/service/backfill"
)

// backfillHandler builds a config from the worker's base settings overlaid with
// the task payload (table / range / filter / runID …) and runs a full
// checkpointed backfill.
type backfillHandler struct {
	cfg    config.Config
	logger *logrus.Logger
}

func (h *backfillHandler) Type() taskqueue.TaskType { return taskqueue.TaskBackfill }

func (h *backfillHandler) Execute(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	cfg := h.cfg // copy; Backfill is a value, so this copies it too
	if err := decodePayload(task.Payload, &cfg.Backfill); err != nil {
		return nil, fmt.Errorf("worker: decode backfill payload: %w", err)
	}
	// Overlay the backfill selection filter (table / events / include /
	// exclude) from the payload — backfill never uses the reporting filter.
	if err := overlayBackfillFilter(task.Payload, &cfg); err != nil {
		return nil, fmt.Errorf("worker: decode backfill filter: %w", err)
	}
	if cfg.Backfill.RunID == "" {
		cfg.Backfill.RunID = task.ID // checkpoint keyed by task id
	}
	cfg.Mode = config.ModeBackfill

	r, err := backfill.New(ctx, cfg, h.logger)
	if err != nil {
		return nil, err
	}
	defer r.Shutdown()
	if err := r.EnsureIndexes(ctx); err != nil {
		return nil, err
	}
	if err := r.Run(ctx); err != nil {
		return nil, err
	}
	s := r.Stats()
	return map[string]any{
		"user_writes":  s.UserWrites.Load(),
		"event_writes": s.EventWrites.Load(),
		"filtered":     s.Filtered.Load(),
		"days_failed":  s.DaysFailed.Load(),
	}, nil
}

// sqlHandler runs an explicit SQL statement from the payload through a one-shot
// backfill executor.
type sqlHandler struct {
	cfg    config.Config
	logger *logrus.Logger
}

func (h *sqlHandler) Type() taskqueue.TaskType { return taskqueue.TaskSQL }

func (h *sqlHandler) Execute(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	sql, _ := task.Payload["sql"].(string)
	if sql == "" {
		return nil, fmt.Errorf("worker: sql task missing 'sql' field")
	}
	cfg := h.cfg
	if t, ok := task.Payload["table"].(string); ok && t != "" {
		cfg.BackfillFilter.Table = t
	}
	if sp, ok := task.Payload["schemaPrefix"].(string); ok {
		cfg.Backfill.SchemaPrefix = sp
	}
	if err := overlayBackfillFilter(task.Payload, &cfg); err != nil {
		return nil, fmt.Errorf("worker: decode sql filter: %w", err)
	}
	cfg.Mode = config.ModeBackfill

	r, err := backfill.NewExecutor(ctx, cfg, h.logger)
	if err != nil {
		return nil, err
	}
	defer r.Shutdown()
	if err := r.EnsureIndexes(ctx); err != nil {
		return nil, err
	}
	rows, err := r.ExecuteSQL(ctx, sql)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rows": rows}, nil
}
