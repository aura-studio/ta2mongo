// Package worker implements the task worker service: a long-running process
// that registers a heartbeat, claims tasks from the shared MongoDB queue,
// executes them (report-sync / backfill / sql), renews the lease while
// working, and reports the outcome.
package worker

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/filter"
	"rocket-nano/tools/tango/internal/core/taskqueue"
	"rocket-nano/tools/tango/internal/service/backfill"
)

// Service is the task worker runtime: it registers a heartbeat, claims
// published tasks, executes them (report-sync / backfill / sql), and reports
// the outcome.
type Service struct {
	cfg      config.Config
	logger   *logrus.Logger
	client   *mongo.Client
	db       *mongo.Database
	queue    *taskqueue.Queue
	registry *taskqueue.Registry
	hostname string
}

// New connects to MongoDB and constructs a worker Service. Caller must call
// Shutdown.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Service, error) {
	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("worker: TANGO_INSTANCE_ID is required")
	}
	mc, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Mongo.URI).SetConnectTimeout(cfg.Mongo.ConnectTimeout).SetServerSelectionTimeout(cfg.Mongo.ServerSelectionTimeout))
	if err != nil {
		return nil, fmt.Errorf("worker: connect mongo: %w", err)
	}
	dbName, err := config.MongoDBFromURI(cfg.Mongo.URI)
	if err != nil {
		_ = mc.Disconnect(context.Background())
		return nil, fmt.Errorf("worker: %w", err)
	}
	db := mc.Database(dbName)
	hostname, _ := os.Hostname()

	return &Service{
		cfg:      cfg,
		logger:   logger,
		client:   mc,
		db:       db,
		queue:    taskqueue.NewQueue(db.Collection(cfg.Worker.TasksCollection)),
		registry: taskqueue.NewRegistry(db.Collection(cfg.Worker.InstancesCollection), cfg.Worker.InstanceTTL),
		hostname: hostname,
	}, nil
}

// Shutdown deregisters the instance and disconnects.
func (a *Service) Shutdown() error {
	dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.registry.Deregister(dctx, a.cfg.InstanceID)
	return a.client.Disconnect(dctx)
}

// EnsureIndexes creates the queue + registry indexes (idempotent).
func (a *Service) EnsureIndexes(ctx context.Context) error {
	if err := a.queue.EnsureIndexes(ctx); err != nil {
		return err
	}
	return a.registry.EnsureIndexes(ctx)
}

// Run blocks until ctx is cancelled, running the heartbeat and claim loops.
func (a *Service) Run(ctx context.Context) error {
	a.logger.WithFields(logrus.Fields{
		"instanceID":    a.cfg.InstanceID,
		"tasks":         a.cfg.Worker.TasksCollection,
		"instances":     a.cfg.Worker.InstancesCollection,
		"poll_interval": a.cfg.Worker.PollInterval,
		"lease":         a.cfg.Worker.LeaseDuration,
	}).Info("worker: started")

	// Register immediately so targeting fail-fast works without waiting for
	// the first heartbeat tick. Retry briefly: a transient failure here would
	// otherwise make this (running) agent look offline to targeted publishers
	// until the first heartbeat-loop tick.
	a.initialHeartbeat(ctx)
	go a.heartbeatLoop(ctx)

	poll := time.NewTicker(a.cfg.Worker.PollInterval)
	defer poll.Stop()

	for {
		// Drain as many tasks as are available before sleeping.
		task, err := a.queue.Claim(ctx, a.cfg.InstanceID, a.cfg.Worker.LeaseDuration)
		switch {
		case err == nil:
			a.runTask(ctx, task)
			continue // try to claim another immediately
		case err == taskqueue.ErrNoTask:
			// nothing to do; run queue maintenance, then wait for next tick.
			a.reap(ctx)
		default:
			a.logger.WithError(err).Warn("worker: claim failed")
		}

		select {
		case <-ctx.Done():
			a.logger.Info("worker: shutting down")
			return nil
		case <-poll.C:
		}
	}
}

// initialHeartbeat registers the instance with a few quick retries so a
// transient error does not leave a live agent looking offline.
func (a *Service) initialHeartbeat(ctx context.Context) {
	for attempt := 0; attempt < 3; attempt++ {
		if err := a.heartbeat(ctx); err == nil {
			return
		} else if attempt == 2 {
			a.logger.WithError(err).Warn("worker: initial heartbeat failed after retries")
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// reap runs queue maintenance: it fails tasks orphaned by crashed agents (lease
// expired + attempts exhausted) and targeted tasks whose target is offline past
// the instance TTL grace window. Errors are logged, not fatal.
func (a *Service) reap(ctx context.Context) {
	n, err := a.queue.Reap(ctx, a.registry, a.cfg.Worker.InstanceTTL)
	if err != nil {
		a.logger.WithError(err).Debug("worker: reap failed")
		return
	}
	if n > 0 {
		a.logger.WithField("reaped", n).Info("worker: reaped stuck/orphaned tasks")
	}
}

func (a *Service) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(a.cfg.Worker.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.heartbeat(ctx); err != nil {
				a.logger.WithError(err).Warn("worker: heartbeat failed")
			}
		}
	}
}

func (a *Service) heartbeat(ctx context.Context) error {
	return a.registry.Heartbeat(ctx, taskqueue.Instance{
		ID:       a.cfg.InstanceID,
		Hostname: a.hostname,
	})
}

// runTask executes one claimed task with lease renewal and reports the result.
func (a *Service) runTask(ctx context.Context, task *taskqueue.Task) {
	log := a.logger.WithFields(logrus.Fields{
		"taskID": task.ID,
		"type":   task.Type,
		"target": task.Target,
	})
	log.Info("worker: claimed task")

	// Renew the lease in the background while the task runs. If renewal fails
	// (the task was reclaimed because we appeared dead), cancel execCtx so we
	// stop wasting work on a task another agent now owns.
	execCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	renewDone := a.startLeaseRenewer(execCtx, cancel, task.ID)

	result, err := a.execute(execCtx, task)

	cancel()    // stop the renewer
	<-renewDone // wait for it to exit

	// Report the outcome with a bounded context so a hung Mongo cannot block
	// shutdown indefinitely (detached from execCtx, which is already cancelled).
	reportCtx, reportCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer reportCancel()

	if err != nil {
		log.WithError(err).Error("worker: task failed")
		if ferr := a.queue.Fail(reportCtx, task, a.cfg.InstanceID, err); ferr != nil {
			log.WithError(ferr).Warn("worker: reporting failure failed")
		}
		return
	}
	if cerr := a.queue.Complete(reportCtx, task.ID, a.cfg.InstanceID, result); cerr != nil {
		log.WithError(cerr).Warn("worker: reporting success failed")
		return
	}
	log.WithField("result", result).Info("worker: task succeeded")
}

// startLeaseRenewer renews the task lease at one third of the lease duration.
// On a failed renewal it cancels the execution context (the lease was lost).
func (a *Service) startLeaseRenewer(ctx context.Context, cancel context.CancelFunc, taskID string) <-chan struct{} {
	done := make(chan struct{})
	interval := a.cfg.Worker.LeaseDuration / 3
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
				if err := a.queue.RenewLease(context.Background(), taskID, a.cfg.InstanceID, a.cfg.Worker.LeaseDuration); err != nil {
					a.logger.WithError(err).WithField("taskID", taskID).
						Warn("worker: lease renewal failed; abandoning task")
					cancel()
					return
				}
			}
		}
	}()
	return done
}

// execute dispatches a task to the right handler and returns a result payload.
func (a *Service) execute(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	switch task.Type {
	case taskqueue.TaskReportSync:
		return a.executeReportSync(ctx, task)
	case taskqueue.TaskBackfill:
		return a.executeBackfill(ctx, task)
	case taskqueue.TaskSQL:
		return a.executeSQL(ctx, task)
	default:
		return nil, fmt.Errorf("worker: unknown task type %q", task.Type)
	}
}

// executeReportSync publishes a new reporting (upload) filter to the control
// plane: it compiles the payload's include/exclude expressions to validate
// them, then writes the remote-config override document. It does NOT apply the
// filter in-process — report services watch the remote-config document and
// hot-reload their own filter.Holder on their next sync tick. The task's
// completion therefore means "written to remote config", not "applied by every
// report service" (see the plan's report-sync semantics note).
func (a *Service) executeReportSync(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	include, exclude := reportSyncFilters(task.Payload)
	if _, err := filter.New(include, exclude); err != nil {
		return nil, fmt.Errorf("worker: report-sync filter does not compile: %w", err)
	}
	// Persist to the remote-config override document so report services converge
	// on the same filter via their sync loop, surviving restarts.
	_, err := a.db.Collection(a.cfg.RemoteConfig.Collection).UpdateOne(ctx,
		bson.M{"_id": a.cfg.RemoteConfig.DocumentID},
		bson.M{"$set": bson.M{"filter": bson.M{"include": include, "exclude": exclude}}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return nil, fmt.Errorf("worker: persist report-sync filter: %w", err)
	}
	return map[string]any{
		"persisted":      true,
		"collection":     a.cfg.RemoteConfig.Collection,
		"documentID":     a.cfg.RemoteConfig.DocumentID,
		"filter_include": include,
		"filter_exclude": exclude,
	}, nil
}

// reportSyncFilters extracts include/exclude expression lists from a report-sync
// payload, accepting either top-level filterInclude/filterExclude arrays or a
// nested filter:{include,exclude} object (the remote-config document shape).
func reportSyncFilters(payload map[string]any) (include, exclude []string) {
	if f, ok := payload["filter"].(map[string]any); ok {
		return toStringSlice(f["include"]), toStringSlice(f["exclude"])
	}
	return toStringSlice(payload["filterInclude"]), toStringSlice(payload["filterExclude"])
}

// executeBackfill builds a config from the agent's base settings overlaid with
// the task payload (table / range / filter / runID …) and runs a full
// checkpointed backfill.
func (a *Service) executeBackfill(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	cfg := a.cfg // copy; Backfill is a value, so this copies it too
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
func (a *Service) executeSQL(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	sql, _ := task.Payload["sql"].(string)
	if sql == "" {
		return nil, fmt.Errorf("worker: sql task missing 'sql' field")
	}
	cfg := a.cfg
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

// overlayBackfillFilter populates the backfill selection filter from a task
// payload. It accepts a nested backfillFilter:{table,events,include,exclude}
// object and/or top-level conveniences (table, events, filterInclude,
// filterExclude). Backfill never uses the reporting filter, so nothing here
// touches cfg.Filter.
func overlayBackfillFilter(payload map[string]any, cfg *config.Config) error {
	if bf, ok := payload["backfillFilter"].(map[string]any); ok {
		if err := decodePayload(bf, &cfg.BackfillFilter); err != nil {
			return err
		}
	}
	if v, ok := payload["table"].(string); ok && v != "" {
		cfg.BackfillFilter.Table = v
	}
	if v, ok := payload["events"]; ok {
		cfg.BackfillFilter.Events = toStringSlice(v)
	}
	if v, ok := payload["filterInclude"]; ok {
		cfg.BackfillFilter.Include = toStringSlice(v)
	}
	if v, ok := payload["filterExclude"]; ok {
		cfg.BackfillFilter.Exclude = toStringSlice(v)
	}
	return nil
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
