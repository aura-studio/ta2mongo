package store

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func (ir *IdentityResolver) findByAccountID(ctx context.Context, accountID string) (*IDMapping, error) {
	var m IDMapping
	err := ir.mapping.FindOne(ctx, bson.M{"#account_id": accountID}).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (ir *IdentityResolver) findByDistinctID(ctx context.Context, distinctID string) (*IDMapping, error) {
	var m IDMapping
	err := ir.mapping.FindOne(ctx, bson.M{"#distinct_ids": distinctID}).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}
