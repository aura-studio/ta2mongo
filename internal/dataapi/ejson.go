package dataapi

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// DecodeRequest parses an Extended JSON v2 request body into a Request. It uses
// the official driver's relaxed-mode parser, which also accepts plain JSON (a
// strict subset of Extended JSON), so the same path handles both
// application/ejson and application/json bodies. Native BSON types written in
// canonical form ($oid, $date, $numberLong, $numberDecimal, ...) are preserved.
func DecodeRequest(data []byte) (*Request, error) {
	var r Request
	if err := bson.UnmarshalExtJSON(data, false, &r); err != nil {
		return nil, fmt.Errorf("dataapi: decode request: %w", err)
	}
	return &r, nil
}

// MarshalEJSON encodes the response as relaxed Extended JSON, so BSON types are
// rendered in their human-friendly relaxed form where possible.
func (r *Response) MarshalEJSON() ([]byte, error) {
	b, err := bson.MarshalExtJSON(r, false, false)
	if err != nil {
		return nil, fmt.Errorf("dataapi: encode response: %w", err)
	}
	return b, nil
}
