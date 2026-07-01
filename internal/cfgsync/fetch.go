package cfgsync

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/dao"
)

// Fetch is the read face of the central config document: it returns the
// document the Watcher converges on (including its monotonic version), or
// (nil, nil) when nothing has been published yet. It is the query counterpart
// to Publish/PublishAppend — gateway GET /config, cli function=configget and
// api.FetchConfig all relay here, so operators can inspect the live remote
// filter before appending to it. cfg may be nil (defaults applied).
func Fetch(ctx context.Context, d *dao.Dao, cfg *Config) (bson.M, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.ApplyDefaults()
	return fetchDoc(ctx, d, DefaultCollection, cfg.DocumentID)
}

// fetchDoc reads the tracked config document via the dao EJSON façade
// (findOne by _id) — reusing the existing read surface, adding no new dao API.
// A missing document (never published) decodes to a nil document and is returned
// as (nil, nil); the Watcher treats that as a no-op (keep current config). This
// is the shared read used by both the startup convergence read and every poll /
// reconcile tick.
func fetchDoc(ctx context.Context, d *dao.Dao, collection, documentID string) (bson.M, error) {
	resp, err := d.EJSON(ctx, &dao.EJSONRequest{
		Action:     dao.EJSONActionFindOne,
		Collection: collection,
		Filter:     bson.M{"_id": documentID},
	})
	if err != nil {
		return nil, err
	}
	return resp.Document, nil
}

// docVersion extracts the monotonic version from a config document. The version
// is decoded as int64 regardless of whether the driver yielded int32, int64, or
// a float (Extended JSON $numberDouble), so a document written by any face reads
// back consistently. Missing or non-numeric → (0, false), which the Watcher
// treats as "ignore this document".
func docVersion(doc bson.M) (int64, bool) {
	switch v := doc["version"].(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}
