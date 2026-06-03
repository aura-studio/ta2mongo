package client

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/filter"
)

// PublishConfig writes (upserts) the remote-config override document that
// tango instances pick up. doc is a partial configuration: only the keys it
// contains override each tango's local YAML, on a per-field basis.
//
// Scope: the document is written to the _tango_config collection of THIS
// client's database (the one in the connection URI). It only affects tango
// processes connected to the same database — a different database is an
// independent namespace with its own config and its own set of instances.
//
// Connection-level keys (mongoURI, remoteConfig, mode) are ignored by
// consumers even if present, so there is no way to redirect a tango's DB
// endpoint or disable its own override loop via the published document.
//
// As a safety check, if doc carries filterInclude/filterExclude they are
// compiled here; a malformed expression is rejected before publishing, so a
// bad filter can never be pushed to every instance at once.
func (c *Client) PublishConfig(ctx context.Context, doc map[string]any) error {
	if len(doc) == 0 {
		return fmt.Errorf("client: PublishConfig: empty document")
	}

	inc, exc := extractFilterExprs(doc)
	if _, err := filter.New(inc, exc); err != nil {
		return fmt.Errorf("client: refusing to publish, filter does not compile: %w", err)
	}

	coll, err := c.configCollection()
	if err != nil {
		return err
	}

	replacement := bson.M{"_id": config.DefaultRemoteConfigDocumentID}
	for k, v := range doc {
		if k == "_id" {
			continue
		}
		replacement[k] = v
	}

	_, err = coll.ReplaceOne(ctx,
		bson.M{"_id": config.DefaultRemoteConfigDocumentID},
		replacement,
		options.Replace().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("client: publish config: %w", err)
	}
	return nil
}

// PublishFilter is a convenience wrapper over PublishConfig that publishes
// just the include/exclude expression lists. Passing nil/empty for either
// clears that side. This is the common control-plane operation: widening or
// narrowing what each tango collects.
func (c *Client) PublishFilter(ctx context.Context, include, exclude []string) error {
	return c.PublishConfig(ctx, map[string]any{
		"filter": map[string]any{
			"include": include,
			"exclude": exclude,
		},
	})
}

// extractFilterExprs digs filter.include / filter.exclude expression lists out
// of a published doc (which mirrors the nested config shape) for pre-publish
// compilation. Returns empty slices when absent.
func extractFilterExprs(doc map[string]any) (include, exclude []string) {
	f, ok := doc["filter"].(map[string]any)
	if !ok {
		return nil, nil
	}
	return toStringSlice(f["include"]), toStringSlice(f["exclude"])
}

// GetPublishedConfig returns the currently published override document, or
// (nil, nil) if none has been published yet.
func (c *Client) GetPublishedConfig(ctx context.Context) (map[string]any, error) {
	coll, err := c.configCollection()
	if err != nil {
		return nil, err
	}
	var raw bson.M
	err = coll.FindOne(ctx, bson.M{"_id": config.DefaultRemoteConfigDocumentID}).Decode(&raw)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("client: get published config: %w", err)
	}
	return map[string]any(raw), nil
}

func (c *Client) configCollection() (*mongo.Collection, error) {
	dbName, err := config.MongoDBFromURI(c.opts.URI)
	if err != nil {
		return nil, fmt.Errorf("client: %w", err)
	}
	return c.client.Database(dbName).Collection(config.DefaultRemoteConfigCollection), nil
}

// toStringSlice coerces a value that may be []string, []any, or bson.A (as
// comes back from BSON) into []string. Non-string elements are skipped. Uses
// reflection so any slice-kind value is handled regardless of its named type.
func toStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	if s, ok := v.([]string); ok {
		return s
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return nil
	}
	out := make([]string, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		if str, ok := rv.Index(i).Interface().(string); ok {
			out = append(out, str)
		}
	}
	return out
}
