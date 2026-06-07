package cfgsync

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestValidatePublishDoc_StripsReservedAndKeepsSubtrees(t *testing.T) {
	set, err := validatePublishDoc(bson.M{
		"_id":     "filter",
		"version": int64(99),
		"filter":  bson.M{"include": bson.A{`#type == "track"`}},
	})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if _, ok := set["_id"]; ok {
		t.Error("_id should be stripped (cfgsync owns it)")
	}
	if _, ok := set["version"]; ok {
		t.Error("version should be stripped (cfgsync owns it)")
	}
	if _, ok := set["filter"]; !ok {
		t.Error("filter subtree should be kept")
	}
}

func TestValidatePublishDoc_RejectsOffAllowlist(t *testing.T) {
	if _, err := validatePublishDoc(bson.M{"dao": bson.M{"mongo": bson.M{"uri": "x"}}}); err == nil {
		t.Fatal("expected rejection of a subtree off the allowlist")
	}
}

func TestValidatePublishDoc_RejectsUncompilableFilter(t *testing.T) {
	if _, err := validatePublishDoc(bson.M{"filter": bson.M{"include": bson.A{`#type ==== "x"`}}}); err == nil {
		t.Fatal("expected rejection of an uncompilable filter")
	}
}

func TestValidatePublishDoc_RejectsEmpty(t *testing.T) {
	if _, err := validatePublishDoc(bson.M{}); err == nil {
		t.Fatal("expected rejection of an empty document")
	}
	if _, err := validatePublishDoc(bson.M{"_id": "filter", "version": int64(1)}); err == nil {
		t.Fatal("expected rejection of a document with no config subtrees")
	}
}
