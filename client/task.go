package client

import (
	"context"
	"fmt"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/filter"
	"rocket-nano/tools/tango/internal/taskqueue"
)

// TaskSpec describes a task to publish.
type TaskSpec struct {
	// Type is the task kind: taskqueue.TaskBackfill or taskqueue.TaskSQL.
	Type taskqueue.TaskType
	// Payload carries task parameters (backfill config fields, or sql/table).
	Payload map[string]any
	// Target restricts execution to a named instance; "" means any instance
	// may run it.
	Target string
	// MaxAttempts caps reclaim retries; 0 uses the queue default.
	MaxAttempts int
	// ID overrides the generated task id (optional, for idempotent publish).
	ID string
}

// PublishTask publishes a task to this client's database task queue.
//
// Scope: like the published config document, a task lives in THIS database and
// is only visible to agents connected to the same database — a different
// database is an independent namespace.
//
// Targeting: when Spec.Target is non-empty, the target instance must currently
// be alive (have a recent heartbeat); otherwise PublishTask fails fast rather
// than enqueueing a task no agent will ever claim. Untargeted tasks (Target
// == "") are claimable by any agent and are never fail-fast checked.
func (c *Client) PublishTask(ctx context.Context, spec TaskSpec) (string, error) {
	if spec.Type == "" {
		return "", fmt.Errorf("client: PublishTask: Type is required")
	}

	q, reg, err := c.taskComponents()
	if err != nil {
		return "", err
	}

	if spec.Target != "" {
		alive, err := reg.IsAlive(ctx, spec.Target)
		if err != nil {
			return "", err
		}
		if !alive {
			return "", fmt.Errorf("client: target instance %q is not online; refusing to publish", spec.Target)
		}
	}

	return q.Publish(ctx, spec.Type, spec.Payload, taskqueue.PublishOptions{
		ID:          spec.ID,
		Target:      spec.Target,
		MaxAttempts: spec.MaxAttempts,
	})
}

// PublishBackfillTask is a convenience wrapper that publishes a structured
// backfill task. payload mirrors the backfill config fields (table,
// partDateRange, eventTimeRange, runID, …); connection settings come from the
// executing agent's local config.
func (c *Client) PublishBackfillTask(ctx context.Context, payload map[string]any, target string) (string, error) {
	return c.PublishTask(ctx, TaskSpec{
		Type:    taskqueue.TaskBackfill,
		Payload: payload,
		Target:  target,
	})
}

// PublishSQLTask publishes a task that runs an explicit SQL statement, routing
// rows to the named table ("user" or "event"). target "" = any agent.
func (c *Client) PublishSQLTask(ctx context.Context, sql, table, target string) (string, error) {
	if sql == "" {
		return "", fmt.Errorf("client: PublishSQLTask: sql is required")
	}
	return c.PublishTask(ctx, TaskSpec{
		Type:    taskqueue.TaskSQL,
		Payload: map[string]any{"sql": sql, "table": table},
		Target:  target,
	})
}

// PublishReportSync publishes a report-sync task: each claiming daemon agent
// applies the given reporting (upload) filter to its live filter and persists
// it as the remote-config override. Passing target "" fans it out to any agent
// (typically you publish one per targeted daemon, or rely on the persisted
// override + each daemon's sync loop). The filter is compiled here so a
// malformed expression is rejected before publishing.
func (c *Client) PublishReportSync(ctx context.Context, include, exclude []string, target string) (string, error) {
	if _, err := filter.New(include, exclude); err != nil {
		return "", fmt.Errorf("client: refusing to publish report-sync, filter does not compile: %w", err)
	}
	return c.PublishTask(ctx, TaskSpec{
		Type: taskqueue.TaskReportSync,
		Payload: map[string]any{
			"filter": map[string]any{"include": include, "exclude": exclude},
		},
		Target: target,
	})
}

// GetTask returns a task by id (nil if absent).
func (c *Client) GetTask(ctx context.Context, taskID string) (*taskqueue.Task, error) {
	q, _, err := c.taskComponents()
	if err != nil {
		return nil, err
	}
	return q.Get(ctx, taskID)
}

// ListInstances returns the currently-online agents (recent heartbeat).
func (c *Client) ListInstances(ctx context.Context) ([]taskqueue.Instance, error) {
	_, reg, err := c.taskComponents()
	if err != nil {
		return nil, err
	}
	return reg.ListAlive(ctx)
}

func (c *Client) taskComponents() (*taskqueue.Queue, *taskqueue.Registry, error) {
	dbName, err := config.MongoDBFromURI(c.opts.URI)
	if err != nil {
		return nil, nil, fmt.Errorf("client: %w", err)
	}
	db := c.client.Database(dbName)
	q := taskqueue.NewQueue(db.Collection(c.opts.TasksCollection))
	reg := taskqueue.NewRegistry(db.Collection(c.opts.InstancesCollection), c.opts.InstanceTTL)
	return q, reg, nil
}
