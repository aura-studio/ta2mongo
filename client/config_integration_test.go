package client

import (
	"context"
	"testing"

	"rocket-nano/tools/tango/config"
)

func TestPublishAndGetConfig(t *testing.T) {
	cli, db, cleanup := testClientSetup(t)
	defer cleanup()
	ctx := context.Background()

	// Nothing published yet.
	got, err := cli.GetPublishedConfig(ctx)
	if err != nil {
		t.Fatalf("GetPublishedConfig (empty): %v", err)
	}
	if got != nil {
		t.Errorf("expected nil before publish, got %v", got)
	}

	// Publish a filter.
	inc := []string{`#type == "track" && #event_name == "PaymentOrderState"`, `#type startsWith "user_"`}
	if err := cli.PublishFilter(ctx, inc, nil); err != nil {
		t.Fatalf("PublishFilter: %v", err)
	}

	// Read it back.
	got, err = cli.GetPublishedConfig(ctx)
	if err != nil {
		t.Fatalf("GetPublishedConfig: %v", err)
	}
	gotInc := toStringSlice(got["filterInclude"])
	if len(gotInc) != 2 || gotInc[0] != inc[0] {
		t.Errorf("filterInclude = %v, want %v", gotInc, inc)
	}

	// Verify it landed in the documented collection/_id.
	var raw map[string]any
	if err := db.Collection(config.DefaultRemoteConfigCollection).
		FindOne(ctx, map[string]any{"_id": config.DefaultRemoteConfigDocumentID}).
		Decode(&raw); err != nil {
		t.Fatalf("direct read: %v", err)
	}

	// Re-publishing replaces (upsert), not duplicates.
	if err := cli.PublishFilter(ctx, []string{`#type == "track"`}, nil); err != nil {
		t.Fatalf("re-publish: %v", err)
	}
	count, _ := db.Collection(config.DefaultRemoteConfigCollection).
		CountDocuments(ctx, map[string]any{})
	if count != 1 {
		t.Errorf("doc count = %d, want 1 (upsert replaces)", count)
	}
	got, _ = cli.GetPublishedConfig(ctx)
	if gi := toStringSlice(got["filterInclude"]); len(gi) != 1 {
		t.Errorf("after re-publish filterInclude = %v, want 1 entry", gi)
	}
}

func TestPublishConfig_RejectsBadFilter(t *testing.T) {
	cli, _, cleanup := testClientSetup(t)
	defer cleanup()
	ctx := context.Background()

	err := cli.PublishFilter(ctx, []string{`#type ==`}, nil) // malformed expr
	if err == nil {
		t.Fatal("expected publish to be rejected for malformed filter")
	}

	// Nothing should have been written.
	got, _ := cli.GetPublishedConfig(ctx)
	if got != nil {
		t.Errorf("bad filter was published anyway: %v", got)
	}
}

func TestPublishConfig_EmptyRejected(t *testing.T) {
	cli, _, cleanup := testClientSetup(t)
	defer cleanup()
	if err := cli.PublishConfig(context.Background(), nil); err == nil {
		t.Error("expected error for empty doc")
	}
}
