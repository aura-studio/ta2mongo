package backfill

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DayStatus tracks the lifecycle of a single partition-date chunk within a
// backfill run.
type DayStatus string

const (
	DayPending    DayStatus = "pending"
	DayInProgress DayStatus = "in_progress"
	DayCompleted  DayStatus = "completed"
	DayFailed     DayStatus = "failed"
)

// DayProgress is the per-day checkpoint state persisted under a run.
type DayProgress struct {
	Status    DayStatus `bson:"status"`
	TaskID    string    `bson:"taskId,omitempty"`
	PageID    int       `bson:"pageId,omitempty"`    // last fully processed page (-1 = none yet)
	PageCount int       `bson:"pageCount,omitempty"` // populated once the task is FINISHED
	Rows      int64     `bson:"rows,omitempty"`      // rows ingested so far for this day
	Error     string    `bson:"error,omitempty"`     // last error if DayFailed
}

// Run is one backfill execution's checkpoint document.
type Run struct {
	ID           string                 `bson:"_id"`
	APIBaseURL   string                 `bson:"apiBaseURL"`
	ProjectID    int                    `bson:"projectID"`
	Table        string                 `bson:"table"`
	StartDate    string                 `bson:"startDate"`
	EndDate      string                 `bson:"endDate"`
	SQLSignature string                 `bson:"sqlSignature"`
	Days         map[string]DayProgress `bson:"days"`
	StartedAt    time.Time              `bson:"startedAt"`
	UpdatedAt    time.Time              `bson:"updatedAt"`
}

// Checkpoint is the persistent progress store. It is safe to call its methods
// from a single goroutine (the runner); concurrent updates are NOT supported
// because the document is loaded into memory and replaced wholesale.
type Checkpoint struct {
	coll *mongo.Collection
	run  *Run
}

// ErrSignatureMismatch is returned when the persisted SQLSignature does not
// match the one computed from the current configuration. The caller should
// either fix the config drift or pick a new RunID.
var ErrSignatureMismatch = errors.New("backfill: SQL signature mismatch (config changed since last run)")

// UserChunkKey is the single chunk identifier used when backfilling user
// tables, which have no partition column and are processed as one task.
const UserChunkKey = "user-full"

// NewCheckpoint opens (or creates) the checkpoint document for the given run.
// It loads existing state from Mongo into memory, verifies the SQL signature
// matches, and refreshes top-level metadata.
//
// startDate/endDate are interpreted as a [start, end] date range when start
// looks like YYYY-MM-DD. When start equals UserChunkKey, the run is treated
// as having a single chunk (no time expansion) — used for user-table sync.
func NewCheckpoint(ctx context.Context, coll *mongo.Collection, runID, apiBaseURL string,
	projectID int, table, startDate, endDate, sqlSignature string,
) (*Checkpoint, error) {
	if runID == "" {
		return nil, errors.New("backfill: checkpoint runID is required")
	}

	cp := &Checkpoint{coll: coll}

	var existing Run
	err := coll.FindOne(ctx, bson.M{"_id": runID}).Decode(&existing)
	switch {
	case err == nil:
		if existing.SQLSignature != sqlSignature {
			return nil, fmt.Errorf("%w: stored=%s wanted=%s",
				ErrSignatureMismatch, existing.SQLSignature, sqlSignature)
		}
		existing.UpdatedAt = time.Now().UTC()
		cp.run = &existing
		// Make sure new days that fall in the range but weren't present
		// before (e.g. the user extended the range) get initialised.
		if cp.fillMissingDays(startDate, endDate) {
			if err := cp.flush(ctx); err != nil {
				return nil, err
			}
		}
		return cp, nil
	case errors.Is(err, mongo.ErrNoDocuments):
		chunks, derr := initChunks(startDate, endDate)
		if derr != nil {
			return nil, derr
		}
		now := time.Now().UTC()
		cp.run = &Run{
			ID:           runID,
			APIBaseURL:   apiBaseURL,
			ProjectID:    projectID,
			Table:        table,
			StartDate:    startDate,
			EndDate:      endDate,
			SQLSignature: sqlSignature,
			Days:         chunks,
			StartedAt:    now,
			UpdatedAt:    now,
		}
		if err := cp.flush(ctx); err != nil {
			return nil, err
		}
		return cp, nil
	default:
		return nil, fmt.Errorf("backfill: load checkpoint: %w", err)
	}
}

// PendingDays returns the partition-dates that still need work, in calendar
// order. Days marked Completed are skipped; InProgress, Pending and Failed
// days are all returned (the runner will resume in-flight days first).
func (cp *Checkpoint) PendingDays() []string {
	out := make([]string, 0, len(cp.run.Days))
	for day, p := range cp.run.Days {
		if p.Status != DayCompleted {
			out = append(out, day)
		}
	}
	sort.Strings(out)
	return out
}

// Day returns the current state for the given partition-date.
func (cp *Checkpoint) Day(day string) DayProgress {
	return cp.run.Days[day]
}

// SetDay overwrites the state for one day and persists the run. The runner
// calls this after every page flush to make resume cheap.
func (cp *Checkpoint) SetDay(ctx context.Context, day string, p DayProgress) error {
	cp.run.Days[day] = p
	cp.run.UpdatedAt = time.Now().UTC()
	return cp.flush(ctx)
}

// MarkCompleted is a shorthand used when a day finishes successfully.
func (cp *Checkpoint) MarkCompleted(ctx context.Context, day string, rows int64) error {
	p := cp.run.Days[day]
	p.Status = DayCompleted
	p.Rows = rows
	p.Error = ""
	return cp.SetDay(ctx, day, p)
}

// MarkFailed is used when a day exhausts retries.
func (cp *Checkpoint) MarkFailed(ctx context.Context, day string, err error) error {
	p := cp.run.Days[day]
	p.Status = DayFailed
	if err != nil {
		p.Error = err.Error()
	}
	return cp.SetDay(ctx, day, p)
}

// flush writes the entire run document back to Mongo (replace-style upsert).
func (cp *Checkpoint) flush(ctx context.Context) error {
	_, err := cp.coll.ReplaceOne(ctx,
		bson.M{"_id": cp.run.ID},
		cp.run,
		options.Replace().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("backfill: flush checkpoint: %w", err)
	}
	return nil
}

func (cp *Checkpoint) fillMissingDays(start, end string) bool {
	wanted, err := initChunks(start, end)
	if err != nil {
		return false
	}
	changed := false
	for day, p := range wanted {
		if _, ok := cp.run.Days[day]; !ok {
			cp.run.Days[day] = p
			changed = true
		}
	}
	return changed
}

// SQLSignature computes a deterministic short hash over the SQL template
// pieces that, if changed, would invalidate prior progress. Callers pass the
// canonicalised inputs (filter SQL, table, range) and we hash them.
func SQLSignature(table string, projectID int, filterWhere, eventTimeStart, eventTimeEnd string) string {
	h := sha256.New()
	h.Write([]byte(table))
	h.Write([]byte("\x00"))
	h.Write([]byte(fmt.Sprintf("%d", projectID)))
	h.Write([]byte("\x00"))
	h.Write([]byte(filterWhere))
	h.Write([]byte("\x00"))
	h.Write([]byte(eventTimeStart))
	h.Write([]byte("\x00"))
	h.Write([]byte(eventTimeEnd))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// initChunks expands the (start, end) pair into a map of Pending entries.
// For a YYYY-MM-DD date range, the result has one entry per day. The special
// value UserChunkKey collapses to a single entry (user-table sync has no
// time partition and runs as one SQL task).
func initChunks(start, end string) (map[string]DayProgress, error) {
	if start == UserChunkKey {
		return map[string]DayProgress{UserChunkKey: {Status: DayPending, PageID: -1}}, nil
	}
	return initDays(start, end)
}

// initDays expands an inclusive [start, end] YYYY-MM-DD range into a map of
// Pending-status DayProgress entries.
func initDays(start, end string) (map[string]DayProgress, error) {
	startT, err := time.Parse("2006-01-02", start)
	if err != nil {
		return nil, fmt.Errorf("parse start: %w", err)
	}
	endT, err := time.Parse("2006-01-02", end)
	if err != nil {
		return nil, fmt.Errorf("parse end: %w", err)
	}
	if endT.Before(startT) {
		return nil, fmt.Errorf("end %s is before start %s", end, start)
	}
	out := make(map[string]DayProgress)
	for d := startT; !d.After(endT); d = d.AddDate(0, 0, 1) {
		out[d.Format("2006-01-02")] = DayProgress{Status: DayPending, PageID: -1}
	}
	return out, nil
}
