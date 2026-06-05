package batch

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestIngest_EnvelopeFormat(t *testing.T) {
	tt, cleanup := testSetup(t)
	defer cleanup()

	ctx := context.Background()

	inner := `{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"env-1","#account_id":"env_acc"}`
	line := `{"level":"info","msg":"` + strings.ReplaceAll(inner, `"`, `\"`) + `"}`
	tt.single(line)

	count, _ := tt.db.Collection("event").CountDocuments(ctx, bson.M{"#uuid": "env-1"})
	if count != 1 {
		t.Errorf("expected 1 event from envelope, got %d", count)
	}
}
