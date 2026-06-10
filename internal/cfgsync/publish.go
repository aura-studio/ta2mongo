package cfgsync

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
)

// Publish modes. Set is the historical behaviour: the published subtrees
// replace the stored ones wholesale ($set per top-level subtree — publishing
// {"filter": {...}} overwrites the entire stored filter, including dropping an
// omitted exclude). Append is read-merge-write: the current document is
// fetched, the published filter rules are UNIONED into the stored ones
// (stored order preserved, exact-duplicate strings skipped, an omitted
// include/exclude side keeps its stored value), and the merged document is
// written back under an optimistic version guard so concurrent appends never
// lose each other's rules.
const (
	PublishModeSet    = "set"
	PublishModeAppend = "append"
)

// publishAppendAttempts bounds the optimistic read-merge-write retry loop. A
// retry only happens while OTHER publishers land writes concurrently (the
// version guard misses); 8 is far beyond realistic contention.
const publishAppendAttempts = 8

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

// PublishWithMode dispatches between the two publish modes. An empty mode means
// PublishModeSet (the historical default); an unknown mode is rejected before
// any read or write.
func PublishWithMode(ctx context.Context, d *dao.Dao, cfg *Config, doc bson.M, mode string) (int64, error) {
	switch mode {
	case "", PublishModeSet:
		return Publish(ctx, d, cfg, doc)
	case PublishModeAppend:
		return PublishAppend(ctx, d, cfg, doc)
	default:
		return 0, fmt.Errorf("cfgsync: publish: unknown mode %q (want %q or %q)", mode, PublishModeSet, PublishModeAppend)
	}
}

// PublishAppend is the merge flavour of Publish: instead of replacing the stored
// subtrees it UNIONS the published filter rules into the stored ones and writes
// the merged document back. The merge is read-modify-write, so it runs under an
// optimistic version guard: the conditional update matches {_id, version=<as
// fetched>} and a miss (a concurrent publish moved the version) re-fetches,
// re-merges and retries — two concurrent appends therefore both land. When no
// document exists yet append degenerates to a first publish (insert version 1);
// an insert lost to a concurrent creator falls back into the merge loop via the
// duplicate-key error. Validation is identical to Publish but runs on the MERGED
// document, so the combined rule set is recompiled before it is stored.
func PublishAppend(ctx context.Context, d *dao.Dao, cfg *Config, doc bson.M) (int64, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.ApplyDefaults()

	// Reject obviously-bad input early (empty doc / reserved-only doc).
	if _, err := validatePublishDoc(doc); err != nil {
		return 0, err
	}

	for attempt := 0; attempt < publishAppendAttempts; attempt++ {
		cur, err := fetchDoc(ctx, d, cfg.Collection, cfg.DocumentID)
		if err != nil {
			return 0, fmt.Errorf("cfgsync: publish append: read current: %w", err)
		}

		if cur == nil {
			// Nothing stored yet: append == first publish. Insert-only (no
			// upsert-$set) so a racing creator is detected via duplicate key
			// and merged with on the retry instead of being overwritten.
			set, err := validatePublishDoc(doc)
			if err != nil {
				return 0, err
			}
			insertDoc := bson.M{"_id": cfg.DocumentID, "version": int64(1)}
			for k, v := range set {
				insertDoc[k] = v
			}
			if _, err := d.EJSON(ctx, &dao.EJSONRequest{
				Action:     dao.EJSONActionInsertOne,
				Collection: cfg.Collection,
				Document:   insertDoc,
			}); err != nil {
				if mongo.IsDuplicateKeyError(err) {
					continue // someone created it concurrently → merge with theirs
				}
				return 0, fmt.Errorf("cfgsync: publish append: create: %w", err)
			}
			return 1, nil
		}

		curVersion, ok := docVersion(cur)
		if !ok {
			return 0, fmt.Errorf("cfgsync: publish append: stored document has no numeric version")
		}

		merged, err := mergePublishDocs(cur, doc)
		if err != nil {
			return 0, err
		}
		// Validate the MERGED result with the same gate as set-mode publish:
		// the combined rule set must compile as a whole.
		set, err := validatePublishDoc(merged)
		if err != nil {
			return 0, err
		}

		resp, err := d.EJSON(ctx, &dao.EJSONRequest{
			Action:     dao.EJSONActionUpdateOne,
			Collection: cfg.Collection,
			Filter:     bson.M{"_id": cfg.DocumentID, "version": curVersion},
			Update:     bson.M{"$set": set, "$inc": bson.M{"version": 1}},
		})
		if err != nil {
			return 0, fmt.Errorf("cfgsync: publish append: %w", err)
		}
		if resp.MatchedCount == nil || *resp.MatchedCount == 0 {
			continue // version moved underneath us → re-fetch and re-merge
		}
		return curVersion + 1, nil
	}
	return 0, fmt.Errorf("cfgsync: publish append: gave up after %d attempts (continuous concurrent publishes)", publishAppendAttempts)
}

// mergePublishDocs unions the published delta into the stored document's config
// subtrees. The filter subtree gets rule-aware treatment: include and exclude
// are merged as ordered sets (stored rules first, new rules appended, exact
// duplicates skipped), and a side omitted by the delta keeps its stored value —
// the defining difference from set-mode, which would drop it. Any other
// (future) subtree in the delta replaces the stored one wholesale; the
// allowlist dry-run in validatePublishDoc still gates it.
func mergePublishDocs(cur, delta bson.M) (bson.M, error) {
	merged := make(bson.M, len(cur)+len(delta))
	for k, v := range cur {
		if _, reserved := reservedFields[k]; reserved {
			continue
		}
		merged[k] = v
	}
	for k, v := range delta {
		if _, reserved := reservedFields[k]; reserved {
			continue
		}
		stored, exists := merged[k]
		if k != SubtreeFilter || !exists {
			merged[k] = v
			continue
		}
		storedSub, err := asSubDocument(k, stored)
		if err != nil {
			return nil, fmt.Errorf("cfgsync: publish append: stored %s: %w", k, err)
		}
		deltaSub, err := asSubDocument(k, v)
		if err != nil {
			return nil, fmt.Errorf("cfgsync: publish append: published %s: %w", k, err)
		}
		mergedSub := bson.M{}
		for sk, sv := range storedSub {
			mergedSub[sk] = sv
		}
		for _, side := range []string{"include", "exclude"} {
			deltaRules, has := deltaSub[side]
			if !has {
				continue // omitted side keeps its stored value
			}
			newRules, err := toStringSlice(deltaRules)
			if err != nil {
				return nil, fmt.Errorf("cfgsync: publish append: filter.%s: %w", side, err)
			}
			oldRules, err := toStringSlice(mergedSub[side])
			if err != nil {
				return nil, fmt.Errorf("cfgsync: publish append: stored filter.%s: %w", side, err)
			}
			mergedSub[side] = unionRules(oldRules, newRules)
		}
		merged[k] = mergedSub
	}
	return merged, nil
}

// unionRules appends the new rules that are not already present, preserving the
// stored order and the new rules' relative order. Comparison is exact-string —
// two semantically-equal but differently-spelled expressions are both kept.
func unionRules(stored, fresh []string) []string {
	seen := make(map[string]struct{}, len(stored))
	out := make([]string, 0, len(stored)+len(fresh))
	for _, r := range stored {
		seen[r] = struct{}{}
		out = append(out, r)
	}
	for _, r := range fresh {
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
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
