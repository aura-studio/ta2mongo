package cfgsync

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
)

// Publish is the write side of cfgsync: it validates a config document against
// the same allowlist + filter compilation the apply side uses, then atomically
// upserts it into the central collection with a monotonically incremented
// version, returning the new version. It is the shared core behind all three
// publish faces (gateway POST /config, cli function=config, api PublishConfig) —
// same-core-many-faces, exactly like upload / ejson / sql.
//
// doc carries only the config content (e.g. {"filter": {"include": [...],
// "exclude": [...]}}); the document identity (_id) and version are owned by
// cfgsync, so any _id/version the caller includes is ignored — _id is set from
// cfg.DocumentID and version is bumped via $inc. Validating here (reject a
// subtree off the allowlist; reject a filter that will not compile) is the first
// of the two gates that keep a bad config out: a bad document never even reaches
// the collection, and even if one did the apply side's last-good fallback is the
// second gate.
//
// cfg supplies the target collection and document id (it must be the same cfg the
// Watcher reads with, or publish and watch would target different documents); a
// nil cfg is defaulted. The write is DocumentDB-safe: a single updateOne with
// $set + $inc and upsert, no pipeline update.
func Publish(ctx context.Context, d *dao.Dao, cfg *Config, doc bson.M) (int64, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.ApplyDefaults()

	set, err := validatePublishDoc(doc)
	if err != nil {
		return 0, err
	}

	if _, err := d.EJSON(ctx, &dao.EJSONRequest{
		Action:     dao.EJSONActionUpdateOne,
		Collection: cfg.Collection,
		Filter:     bson.M{"_id": cfg.DocumentID},
		Update:     bson.M{"$set": set, "$inc": bson.M{"version": 1}},
		Upsert:     true,
	}); err != nil {
		return 0, fmt.Errorf("cfgsync: publish: %w", err)
	}

	// Read back the new version. The write above is atomic; this follow-up read is
	// only to report the resulting version (it may already reflect a concurrent
	// later publish, which is fine — versions only ever move forward).
	updated, err := fetchDoc(ctx, d, cfg.Collection, cfg.DocumentID)
	if err != nil {
		return 0, fmt.Errorf("cfgsync: publish read-back: %w", err)
	}
	version, _ := docVersion(updated)
	return version, nil
}

// validatePublishDoc strips the cfgsync-owned fields (_id, version) from doc and
// validates the remaining subtrees by replaying them through the same Registry +
// appliers the Watcher uses (against a throwaway parser), so the publish-side and
// apply-side checks can never drift: an off-allowlist subtree or an
// uncompilable filter is rejected here exactly as it would be on apply. It
// returns the sanitised $set payload.
func validatePublishDoc(doc bson.M) (bson.M, error) {
	if len(doc) == 0 {
		return nil, fmt.Errorf("cfgsync: publish: empty config document")
	}
	set := make(bson.M, len(doc))
	for k, v := range doc {
		if _, reserved := reservedFields[k]; reserved {
			continue // cfgsync owns _id and version
		}
		set[k] = v
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("cfgsync: publish: document has no config subtrees")
	}

	// Dry-run the appliers against a throwaway parser: this both enforces the
	// allowlist and compiles the filter, returning the same error apply would.
	reg := NewRegistry()
	RegisterFilter(reg, parser.New(nil))
	if err := reg.Apply(set); err != nil {
		return nil, fmt.Errorf("cfgsync: publish rejected: %w", err)
	}
	return set, nil
}
