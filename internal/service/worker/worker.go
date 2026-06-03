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

	"github.com/sirupsen/logrus"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/runtime"
	"rocket-nano/tools/tango/internal/core/taskqueue"
)

// Service is the task worker runtime: it registers a heartbeat, claims
// published tasks, executes them (report-sync / backfill / sql), and reports
// the outcome.
type Service struct {
	cfg      config.Config
	logger   *logrus.Logger
	mongo    *runtime.MongoResource
	queue    *taskqueue.Queue
	registry *taskqueue.Registry
	hostname string
	handlers map[taskqueue.TaskType]Handler
}

// New connects to MongoDB and constructs a worker Service. Caller must call
// Shutdown.
func New(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Service, error) {
	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("worker: TANGO_INSTANCE_ID is required")
	}
	res, err := runtime.ConnectMongo(ctx, cfg.Mongo)
	if err != nil {
		return nil, fmt.Errorf("worker: %w", err)
	}
	db := res.DB
	hostname, _ := os.Hostname()

	return &Service{
		cfg:      cfg,
		logger:   logger,
		mongo:    res,
		queue:    taskqueue.NewQueue(db.Collection(cfg.Worker.TasksCollection)),
		registry: taskqueue.NewRegistry(db.Collection(cfg.Worker.InstancesCollection), cfg.Worker.InstanceTTL),
		hostname: hostname,
		handlers: buildHandlers(cfg, db, logger),
	}, nil
}

// Shutdown deregisters the instance and disconnects.
func (a *Service) Shutdown() error {
	dctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.registry.Deregister(dctx, a.cfg.InstanceID)
	return a.mongo.Close()
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

// execute dispatches a task to its registered handler and returns the result
// payload. Handlers are pure executors; the surrounding claim/lease/report
// lifecycle stays in runTask.
func (a *Service) execute(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	h, ok := a.handlers[task.Type]
	if !ok {
		return nil, fmt.Errorf("worker: unknown task type %q", task.Type)
	}
	return h.Execute(ctx, task)
}
