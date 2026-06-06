package sql

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// MarshalEJSON encodes the result as relaxed Extended JSON. SELECT rows carry
// native BSON types (ObjectId, Date, ...), so EJSON is used to render them
// faithfully — matching the gateway /ejson response convention.
func (r *Result) MarshalEJSON() ([]byte, error) {
	b, err := bson.MarshalExtJSON(r, false, false)
	if err != nil {
		return nil, fmt.Errorf("sql: encode result: %w", err)
	}
	return b, nil
}
