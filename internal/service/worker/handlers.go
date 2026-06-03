package worker

import (
	"context"

	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/taskqueue"
)

// Handler executes one kind of task. Handlers are pure executors: they receive
// a claimed task and return a result payload or an error. They never claim,
// complete, fail, or renew tasks — the worker Service owns that lifecycle.
type Handler interface {
	Type() taskqueue.TaskType
	Execute(ctx context.Context, task *taskqueue.Task) (map[string]any, error)
}

// buildHandlers wires the task handlers into a type-indexed registry. Adding a
// new task type means adding a Handler here, not extending a switch.
func buildHandlers(cfg config.Config, db *mongo.Database, logger *logrus.Logger) map[taskqueue.TaskType]Handler {
	list := []Handler{
		&reportSyncHandler{
			coll:       db.Collection(cfg.RemoteConfig.Collection),
			documentID: cfg.RemoteConfig.DocumentID,
		},
		&backfillHandler{cfg: cfg, logger: logger},
		&sqlHandler{cfg: cfg, logger: logger},
	}
	m := make(map[taskqueue.TaskType]Handler, len(list))
	for _, h := range list {
		m[h.Type()] = h
	}
	return m
}
