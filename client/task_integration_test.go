package client

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/taskqueue"
)

func TestPublishSQLTask_Untargeted(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()
	ctx := context.Background()

	id, err := cli.PublishSQLTask(ctx, "SELECT * FROM ta.v_user_35", "user", "")
	if err != nil {
		t.Fatalf("PublishSQLTask: %v", err)
	}

	task, err := cli.GetTask(ctx, id)
	if err != nil || task == nil {
		t.Fatalf("GetTask: %v task=%v", err, task)
	}
	if task.Type != taskqueue.TaskSQL || task.Target != "" || task.Status != taskqueue.StatusPending {
		t.Errorf("unexpected task: %+v", task)
	}
	if task.Payload["sql"] != "SELECT * FROM ta.v_user_35" || task.Payload["table"] != "user" {
		t.Errorf("payload = %v", task.Payload)
	}

	// Landed in the documented collection.
	n, _ := db.Collection(config.DefaultTasksCollection).CountDocuments(ctx, bson.M{})
	if n != 1 {
		t.Errorf("task count = %d, want 1", n)
	}
}

func TestPublishTask_TargetOfflineRejected(t *testing.T) {
	cli, _, cleanup := testClientSetup(t)
	defer cleanup()
	ctx := context.Background()

	// No agent has registered, so targeting one must fail fast.
	_, err := cli.PublishBackfillTask(ctx, map[string]any{"table": "user"}, "ghost-instance")
	if err == nil {
		t.Fatal("expected publish to a non-online target to fail")
	}
}

func TestPublishTask_TargetOnlineAccepted(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()
	ctx := context.Background()

	// Simulate an online agent by registering a fresh heartbeat.
	reg := taskqueue.NewRegistry(db.Collection(config.DefaultInstancesCollection), config.DefaultInstanceTTL)
	if err := reg.Heartbeat(ctx, taskqueue.Instance{ID: "agent-online"}); err != nil {
		t.Fatal(err)
	}

	id, err := cli.PublishBackfillTask(ctx, map[string]any{"table": "user"}, "agent-online")
	if err != nil {
		t.Fatalf("publish to online target: %v", err)
	}
	task, _ := cli.GetTask(ctx, id)
	if task == nil || task.Target != "agent-online" {
		t.Errorf("unexpected task: %+v", task)
	}
}

func TestListInstances(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()
	ctx := context.Background()

	reg := taskqueue.NewRegistry(db.Collection(config.DefaultInstancesCollection), config.DefaultInstanceTTL)
	_ = reg.Heartbeat(ctx, taskqueue.Instance{ID: "a1"})
	_ = reg.Heartbeat(ctx, taskqueue.Instance{ID: "a2"})

	list, err := cli.ListInstances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("ListInstances = %d, want 2", len(list))
	}
	_ = time.Now()
}
