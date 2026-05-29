// Package agent implements the `tango agent` task-worker mode: a long-running
// process that registers a heartbeat, claims tasks from the shared MongoDB
// queue, executes them (backfill / sql), renews the lease while working, and
// reports the outcome.
package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/backfill"
	"rocket-nano/tools/tango/internal/taskqueue"
)

// Agent is the task-worker runtime.
type Agent struct {
	cfg      config.Config
	logger   *logrus.Logger
	client   *mongo.Client
	queue    *taskqueue.Queue
	registry *taskqueue.Registry
	hostname string
}

// New connects to MongoDB and constructs an Agent. Caller must call Shutdown.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Agent, error) {
	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("agent: TANGO_INSTANCE_ID is required")
	}
	mc, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Mongo.URI).SetConnectTimeout(cfg.Mongo.ConnectTimeout).SetServerSelectionTimeout(cfg.Mongo.ServerSelectionTimeout))
	if err != nil {
		return nil, fmt.Errorf("agent: connect mongo: %w", err)
	}
	dbName, err := config.MongoDBFromURI(cfg.Mongo.URI)
	if err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("agent: %w", err)
	}
	db := mc.Database(dbName)
	hostname, _ := os.Hostname()

	return &Agent{
		cfg:      cfg,
		logger:   logger,
		client:   mc,
		queue:    taskqueue.NewQueue(db.Collection(cfg.Agent.TasksCollection)),
		registry: taskqueue.NewRegistry(db.Collection(cfg.Agent.InstancesCollection), cfg.Agent.InstanceTTL),
		hostname: hostname,
	}, nil
}

// Shutdown deregisters the instance and disconnects.
func (a *Agent) Shutdown() error {
	dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.registry.Deregister(dctx, a.cfg.InstanceID)
	return a.client.Disconnect(dctx)
}

// EnsureIndexes creates the queue + registry indexes (idempotent).
func (a *Agent) EnsureIndexes(ctx context.Context) error {
	if err := a.queue.EnsureIndexes(ctx); err != nil {
		return err
	}
	return a.registry.EnsureIndexes(ctx)
}

// Run blocks until ctx is cancelled, running the heartbeat and claim loops.
func (a *Agent) Run(ctx context.Context) error {
	a.logger.WithFields(logrus.Fields{
		"instanceID":    a.cfg.InstanceID,
		"tasks":         a.cfg.Agent.TasksCollection,
		"instances":     a.cfg.Agent.InstancesCollection,
		"poll_interval": a.cfg.Agent.PollInterval,
		"lease":         a.cfg.Agent.LeaseDuration,
	}).Info("agent: started")

	// Register immediately so targeting fail-fast works without waiting for
	// the first heartbeat tick.
	if err := a.heartbeat(ctx); err != nil {
		a.logger.WithError(err).Warn("agent: initial heartbeat failed")
	}
	go a.heartbeatLoop(ctx)

	poll := time.NewTicker(a.cfg.Agent.PollInterval)
	defer poll.Stop()

	for {
		// Drain as many tasks as are available before sleeping.
		task, err := a.queue.Claim(ctx, a.cfg.InstanceID, a.cfg.Agent.LeaseDuration)
		switch {
		case err == nil:
			a.runTask(ctx, task)
			continue // try to claim another immediately
		case err == taskqueue.ErrNoTask:
			// nothing to do; wait for next tick
		default:
			a.logger.WithError(err).Warn("agent: claim failed")
		}

		select {
		case <-ctx.Done():
			a.logger.Info("agent: shutting down")
			return nil
		case <-poll.C:
		}
	}
}

func (a *Agent) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(a.cfg.Agent.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.heartbeat(ctx); err != nil {
				a.logger.WithError(err).Warn("agent: heartbeat failed")
			}
		}
	}
}

func (a *Agent) heartbeat(ctx context.Context) error {
	return a.registry.Heartbeat(ctx, taskqueue.Instance{
		ID:       a.cfg.InstanceID,
		Hostname: a.hostname,
	})
}

// runTask executes one claimed task with lease renewal and reports the result.
func (a *Agent) runTask(ctx context.Context, task *taskqueue.Task) {
	log := a.logger.WithFields(logrus.Fields{
		"taskID": task.ID,
		"type":   task.Type,
		"target": task.Target,
	})
	log.Info("agent: claimed task")

	// Renew the lease in the background while the task runs. If renewal fails
	// (the task was reclaimed because we appeared dead), cancel execCtx so we
	// stop wasting work on a task another agent now owns.
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewDone := a.startLeaseRenewer(execCtx, cancel, task.ID)

	result, err := a.execute(execCtx, task)

	cancel()    // stop the renewer
	<-renewDone // wait for it to exit

	if err != nil {
		log.WithError(err).Error("agent: task failed")
		if ferr := a.queue.Fail(context.Background(), task, a.cfg.InstanceID, err); ferr != nil {
			log.WithError(ferr).Warn("agent: reporting failure failed")
		}
		return
	}
	if cerr := a.queue.Complete(context.Background(), task.ID, a.cfg.InstanceID, result); cerr != nil {
		log.WithError(cerr).Warn("agent: reporting success failed")
		return
	}
	log.WithField("result", result).Info("agent: task succeeded")
}

// startLeaseRenewer renews the task lease at one third of the lease duration.
// On a failed renewal it cancels the execution context (the lease was lost).
func (a *Agent) startLeaseRenewer(ctx context.Context, cancel context.CancelFunc, taskID string) <-chan struct{} {
	done := make(chan struct{})
	interval := a.cfg.Agent.LeaseDuration / 3
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := a.queue.RenewLease(context.Background(), taskID, a.cfg.InstanceID, a.cfg.Agent.LeaseDuration); err != nil {
					a.logger.WithError(err).WithField("taskID", taskID).
						Warn("agent: lease renewal failed; abandoning task")
					cancel()
					return
				}
			}
		}
	}()
	return done
}

// execute dispatches a task to the right handler and returns a result payload.
func (a *Agent) execute(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	switch task.Type {
	case taskqueue.TaskBackfill:
		return a.executeBackfill(ctx, task)
	case taskqueue.TaskSQL:
		return a.executeSQL(ctx, task)
	default:
		return nil, fmt.Errorf("agent: unknown task type %q", task.Type)
	}
}

// executeBackfill builds a config from the agent's base settings overlaid with
// the task payload (table / range / filter / runID …) and runs a full
// checkpointed backfill.
func (a *Agent) executeBackfill(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	cfg := a.cfg // copy; Backfill is a value, so this copies it too
	if err := decodePayload(task.Payload, &cfg.Backfill); err != nil {
		return nil, fmt.Errorf("agent: decode backfill payload: %w", err)
	}
	// Allow the task to set top-level filter expressions too.
	overlayFilters(task.Payload, &cfg)
	if cfg.Backfill.RunID == "" {
		cfg.Backfill.RunID = task.ID // checkpoint keyed by task id
	}
	cfg.Mode = config.ModeBackfill

	r, err := backfill.New(ctx, cfg, a.logger)
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

// executeSQL runs an explicit SQL statement from the payload.
func (a *Agent) executeSQL(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	sql, _ := task.Payload["sql"].(string)
	if sql == "" {
		return nil, fmt.Errorf("agent: sql task missing 'sql' field")
	}
	cfg := a.cfg
	if t, ok := task.Payload["table"].(string); ok && t != "" {
		cfg.Backfill.Table = t
	}
	if sp, ok := task.Payload["schemaPrefix"].(string); ok {
		cfg.Backfill.SchemaPrefix = sp
	}
	overlayFilters(task.Payload, &cfg)
	cfg.Mode = config.ModeBackfill

	r, err := backfill.NewExecutor(ctx, cfg, a.logger)
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

// decodePayload maps the task payload onto a struct via mapstructure with the
// duration/slice hooks, leaving unset fields at their incoming value.
func decodePayload(payload map[string]any, target any) error {
	if len(payload) == 0 {
		return nil
	}
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           target,
		TagName:          "mapstructure",
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return err
	}
	return dec.Decode(payload)
}

// overlayFilters lets a task payload set top-level filterInclude/filterExclude.
func overlayFilters(payload map[string]any, cfg *config.Config) {
	if v, ok := payload["filterInclude"]; ok {
		cfg.Filter.Include = toStringSlice(v)
	}
	if v, ok := payload["filterExclude"]; ok {
		cfg.Filter.Exclude = toStringSlice(v)
	}
}

func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}
