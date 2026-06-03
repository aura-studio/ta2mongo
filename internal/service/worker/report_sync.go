package worker

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/internal/core/filter"
	"rocket-nano/tools/tango/internal/core/taskqueue"
)

// reportSyncHandler publishes a new reporting (upload) filter to the control
// plane: it compiles the payload's include/exclude expressions to validate
// them, then writes the remote-config override document. It does NOT apply the
// filter in-process — report services watch the remote-config document and
// hot-reload their own filter.Holder on their next sync tick. Completion
// therefore means "written to remote config", not "applied by every report
// service" (see the plan's report-sync semantics note).
type reportSyncHandler struct {
	coll       *mongo.Collection
	documentID string
}

func (h *reportSyncHandler) Type() taskqueue.TaskType { return taskqueue.TaskReportSync }

func (h *reportSyncHandler) Execute(ctx context.Context, task *taskqueue.Task) (map[string]any, error) {
	include, exclude := reportSyncFilters(task.Payload)
	if _, err := filter.New(include, exclude); err != nil {
		return nil, fmt.Errorf("worker: report-sync filter does not compile: %w", err)
	}
	// Persist to the remote-config override document so report services converge
	// on the same filter via their sync loop, surviving restarts.
	_, err := h.coll.UpdateOne(ctx,
		bson.M{"_id": h.documentID},
		bson.M{"$set": bson.M{"filter": bson.M{"include": include, "exclude": exclude}}},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return nil, fmt.Errorf("worker: persist report-sync filter: %w", err)
	}
	return map[string]any{
		"persisted":      true,
		"collection":     h.coll.Name(),
		"documentID":     h.documentID,
		"filter_include": include,
		"filter_exclude": exclude,
	}, nil
}
