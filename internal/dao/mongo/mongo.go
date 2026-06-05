// Package mongo assembles the shared MongoDB resources used by the service
// and process layers — chiefly the MongoDB connection, the resolved database,
// and the Store — so that connection setup, database resolution, and lifecycle
// ownership are expressed once instead of being copy-pasted into every service
// constructor.
package mongo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// MongoResource bundles a MongoDB client with its resolved database. Close
// disconnects the client.
type MongoResource struct {
	Client *mongo.Client
	DB     *mongo.Database
}

// ConnectMongo opens a MongoDB connection from the given config, verifies it
// with a Ping, resolves the database from the URI path, and returns an owned
// MongoResource. The caller must Close it.
func ConnectMongo(ctx context.Context, cfg *Config) (*MongoResource, error) {
	client, err := mongo.Connect(options.Client().
		ApplyURI(cfg.URI).
		SetConnectTimeout(cfg.ConnectTimeout).
		SetServerSelectionTimeout(cfg.ServerSelectionTimeout))
	if err != nil {
		return nil, fmt.Errorf("mongo: connect: %w", err)
	}
	// mongo.Connect is lazy, so a Ping forces server selection: an unreachable or
	// misconfigured server fails here (bounded by ServerSelectionTimeout) instead
	// of surfacing later on the first real operation.
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongo: ping: %w", err)
	}
	db, err := DatabaseFromClient(client, cfg.URI)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	return &MongoResource{Client: client, DB: db}, nil
}

// DatabaseFromClient resolves the database named in the URI path on the given
// client.
func DatabaseFromClient(client *mongo.Client, uri string) (*mongo.Database, error) {
	name, err := MongoDBFromURI(uri)
	if err != nil {
		return nil, fmt.Errorf("mongo: %w", err)
	}
	return client.Database(name), nil
}

// Close disconnects the client. It is safe to call on a nil receiver.
func (r *MongoResource) Close() error {
	if r == nil || r.Client == nil {
		return nil
	}
	return r.Client.Disconnect(context.Background())
}
