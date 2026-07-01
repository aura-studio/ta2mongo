package store

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// EnsureIndexes creates all required indexes on the user, event,
// dead_letter, and id_mapping collections. This is safe to call repeatedly (idempotent).
func (s *Store) EnsureIndexes(ctx context.Context) error {
	if err := s.identity.EnsureIndexes(ctx); err != nil {
		return err
	}
	if err := s.ensureUserIndexes(ctx); err != nil {
		return err
	}
	if err := s.ensureEventIndexes(ctx); err != nil {
		return err
	}
	return s.ensureDeadLetterIndexes(ctx)
}

func (s *Store) ensureUserIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "#user_id", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "#account_id", Value: 1}}},
		{Keys: bson.D{{Key: "#distinct_id", Value: 1}}},
		{Keys: bson.D{{Key: "_ts", Value: 1}}},
	}
	_, err := s.user.Indexes().CreateMany(ctx, indexes)
	return err
}

func (s *Store) ensureEventIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "#event_name", Value: 1},
				{Key: "#account_id", Value: 1},
				{Key: "#time", Value: 1},
			},
		},
		{
			Keys: bson.D{
				{Key: "#event_name", Value: 1},
				{Key: "#distinct_id", Value: 1},
				{Key: "#time", Value: 1},
			},
		},
		{
			Keys: bson.D{
				{Key: "#event_name", Value: 1},
				{Key: "#time", Value: 1},
			},
		},
		{
			Keys:    bson.D{{Key: "#uuid", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "_ts", Value: 1}}},
	}
	_, err := s.event.Indexes().CreateMany(ctx, indexes)
	return err
}

func (s *Store) ensureDeadLetterIndexes(ctx context.Context) error {
	indexes := []mongo.IndexModel{
		{Keys: bson.D{{Key: "_ts", Value: 1}}},
	}
	_, err := s.deadLetter.Indexes().CreateMany(ctx, indexes)
	return err
}
