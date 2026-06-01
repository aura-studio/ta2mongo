package taskqueue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Instance is a heartbeat registration document for one agent. It exists so
// that targeted publishing can fail fast when the target is offline and so
// operators can list live agents; task-claim correctness does NOT depend on
// it (that is handled by atomic claim + lease).
type Instance struct {
	ID        string    `bson:"_id"`
	LastSeen  time.Time `bson:"lastSeen"`
	StartedAt time.Time `bson:"startedAt"`
	Hostname  string    `bson:"hostname,omitempty"`
}

// Registry manages agent heartbeat documents with a TTL index so that stale
// registrations disappear automatically.
type Registry struct {
	coll *mongo.Collection
	ttl  time.Duration
}

// NewRegistry wraps a collection as an instance registry with the given TTL.
func NewRegistry(coll *mongo.Collection, ttl time.Duration) *Registry {
	return &Registry{coll: coll, ttl: ttl}
}

// EnsureIndexes creates the TTL index on lastSeen. MongoDB's TTL monitor
// deletes a document once lastSeen is older than ttl, so an agent that stops
// heart-beating is automatically dropped from the registry.
//
// Note: Amazon DocumentDB supports TTL indexes; if the deployment does not,
// IsAlive still works by comparing lastSeen against ttl in the query, so
// correctness does not rely on the background TTL deleter.
func (r *Registry) EnsureIndexes(ctx context.Context) error {
	_, err := r.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "lastSeen", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(int32(r.ttl.Seconds())),
	})
	// A pre-existing index with a different TTL makes CreateOne fail; that is
	// not fatal for operation, so swallow the conflict.
	if err != nil && !isIndexConflict(err) {
		return err
	}
	return nil
}

// Heartbeat upserts this instance's registration with a fresh lastSeen.
func (r *Registry) Heartbeat(ctx context.Context, inst Instance) error {
	now := time.Now().UTC()
	inst.LastSeen = now
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": inst.ID},
		bson.M{
			"$set":         bson.M{"lastSeen": now, "hostname": inst.Hostname},
			"$setOnInsert": bson.M{"startedAt": now},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("taskqueue: heartbeat: %w", err)
	}
	return nil
}

// IsAlive reports whether the named instance has a heartbeat newer than the
// TTL. This does not rely on the TTL deleter having run.
func (r *Registry) IsAlive(ctx context.Context, instanceID string) (bool, error) {
	cutoff := time.Now().UTC().Add(-r.ttl)
	n, err := r.coll.CountDocuments(ctx, bson.M{
		"_id":      instanceID,
		"lastSeen": bson.M{"$gte": cutoff},
	})
	if err != nil {
		return false, fmt.Errorf("taskqueue: is-alive: %w", err)
	}
	return n > 0, nil
}

// ListAlive returns all instances with a heartbeat newer than the TTL.
func (r *Registry) ListAlive(ctx context.Context) ([]Instance, error) {
	cutoff := time.Now().UTC().Add(-r.ttl)
	cur, err := r.coll.Find(ctx, bson.M{"lastSeen": bson.M{"$gte": cutoff}})
	if err != nil {
		return nil, fmt.Errorf("taskqueue: list-alive: %w", err)
	}
	defer cur.Close(ctx)
	var out []Instance
	if err := cur.All(ctx, &out); err != nil {
		return nil, fmt.Errorf("taskqueue: list-alive decode: %w", err)
	}
	return out, nil
}

// Deregister removes this instance's heartbeat document (best-effort, called
// on graceful shutdown).
func (r *Registry) Deregister(ctx context.Context, instanceID string) error {
	_, err := r.coll.DeleteOne(ctx, bson.M{"_id": instanceID})
	return err
}

// isIndexConflict reports whether the error is a benign "index already exists
// with different options" — e.g. when the TTL was changed between runs.
func isIndexConflict(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "IndexOptionsConflict") ||
		strings.Contains(msg, "already exists with different options") ||
		strings.Contains(msg, "IndexKeySpecsConflict")
}
