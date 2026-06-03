// Package taskqueue implements a MongoDB-backed task queue with optional
// instance targeting, used by the daemon's agent feature (agent.enabled).
//
// Design (pull model, no central scheduler, no heartbeat dependency for
// correctness):
//
//   - A task is a document in the tasks collection with a status and an
//     optional Target instance id. Target == "" means "any agent may run it";
//     a non-empty Target restricts execution to the named instance.
//   - Agents claim work with an atomic findOneAndUpdate that flips a
//     pending (or lease-expired) task to claimed and stamps a lease. The
//     atomicity guarantees exactly one agent owns a task at a time, so two
//     agents never run the same task concurrently.
//   - A long-running task renews its lease periodically. If the owning agent
//     dies, the lease expires and another agent reclaims the task — this is
//     what removes the need for heartbeat-based liveness in the hot path.
//
// The separate instance registry (instance.go) exists only for targeting
// fail-fast and operator visibility, not for task-claim correctness.
package taskqueue

import "time"

// TaskType enumerates the kinds of work an agent can execute.
type TaskType string

const (
	// TaskBackfill runs a structured historical backfill (the agent builds the
	// SQL from the payload's table/projectID/range/filter).
	TaskBackfill TaskType = "backfill"
	// TaskSQL runs an explicit SQL statement supplied in the payload and
	// imports the rows through the same streaming pipeline as backfill.
	TaskSQL TaskType = "sql"
	// TaskReportSync pushes a new reporting (upload) filter to daemons: the
	// agent applies the payload's filter to its daemon's live filter and
	// persists it as the remote-config override so restarts converge.
	TaskReportSync TaskType = "report-sync"
)

// TaskStatus is the lifecycle state of a task.
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusClaimed   TaskStatus = "claimed"
	StatusSucceeded TaskStatus = "succeeded"
	StatusFailed    TaskStatus = "failed"
)

// Task is one unit of work in the queue.
type Task struct {
	ID          string         `bson:"_id"`
	Type        TaskType       `bson:"type"`
	Payload     map[string]any `bson:"payload"`
	Target      string         `bson:"target"` // "" = any instance
	Status      TaskStatus     `bson:"status"`
	ClaimedBy   string         `bson:"claimedBy,omitempty"`
	LeaseUntil  time.Time      `bson:"leaseUntil,omitempty"`
	Attempts    int            `bson:"attempts"`
	MaxAttempts int            `bson:"maxAttempts"`
	Result      map[string]any `bson:"result,omitempty"`
	Error       string         `bson:"error,omitempty"`
	CreatedAt   time.Time      `bson:"createdAt"`
	UpdatedAt   time.Time      `bson:"updatedAt"`
	// NotBefore gates re-claiming after a retry: a failed-with-retry task is
	// not claimable again until now >= NotBefore (exponential backoff). Zero
	// means immediately claimable.
	NotBefore time.Time `bson:"notBefore,omitempty"`
	// StartedAt is the time of the FIRST claim and is preserved across
	// reclaims (set once via $ifNull in the claim pipeline).
	StartedAt  *time.Time `bson:"startedAt,omitempty"`
	FinishedAt *time.Time `bson:"finishedAt,omitempty"`
}
