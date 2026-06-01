package pipeline

import (
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestNewBatch(t *testing.T) {
	b := NewBatch(10)
	if b == nil {
		t.Fatal("expected non-nil batch")
	}
	if b.Len() != 0 {
		t.Errorf("expected len=0, got %d", b.Len())
	}
	if !b.Empty() {
		t.Error("expected empty batch")
	}
	if b.Full() {
		t.Error("expected not full batch")
	}
}

func TestBatch_AddAndLen(t *testing.T) {
	b := NewBatch(5)

	for i := 0; i < 3; i++ {
		b.Add(mongo.NewInsertOneModel().SetDocument(bson.M{"i": i}))
	}

	if b.Len() != 3 {
		t.Errorf("expected len=3, got %d", b.Len())
	}
	if b.Empty() {
		t.Error("expected non-empty batch")
	}
	if b.Full() {
		t.Error("expected not full yet (3 < 5)")
	}
}

func TestBatch_Full(t *testing.T) {
	b := NewBatch(3)
	for i := 0; i < 3; i++ {
		b.Add(mongo.NewInsertOneModel().SetDocument(bson.M{"i": i}))
	}

	if !b.Full() {
		t.Error("expected batch to be full at capacity")
	}
}

func TestBatch_OverCapacity(t *testing.T) {
	b := NewBatch(2)
	for i := 0; i < 5; i++ {
		b.Add(mongo.NewInsertOneModel().SetDocument(bson.M{"i": i}))
	}

	if b.Len() != 5 {
		t.Errorf("expected len=5 (overflow allowed), got %d", b.Len())
	}
	if !b.Full() {
		t.Error("expected full when over capacity")
	}
}

func TestBatch_Reset(t *testing.T) {
	b := NewBatch(10)
	for i := 0; i < 5; i++ {
		b.Add(mongo.NewInsertOneModel().SetDocument(bson.M{"i": i}))
	}

	b.Reset()
	if b.Len() != 0 {
		t.Errorf("expected len=0 after reset, got %d", b.Len())
	}
	if !b.Empty() {
		t.Error("expected empty after reset")
	}
	if b.Full() {
		t.Error("expected not full after reset")
	}
}

func TestBatch_ResetPreservesCapacity(t *testing.T) {
	b := NewBatch(10)
	for i := 0; i < 10; i++ {
		b.Add(mongo.NewInsertOneModel().SetDocument(bson.M{"i": i}))
	}

	b.Reset()

	// Add again and verify it works
	for i := 0; i < 3; i++ {
		b.Add(mongo.NewInsertOneModel().SetDocument(bson.M{"i": i}))
	}
	if b.Len() != 3 {
		t.Errorf("expected len=3 after reset+add, got %d", b.Len())
	}
}

func TestBatch_ZeroCapacity(t *testing.T) {
	b := NewBatch(0)
	if b == nil {
		t.Fatal("expected non-nil batch even with 0 capacity")
	}
	// Adding to a 0-capacity batch should immediately be "full"
	b.Add(mongo.NewInsertOneModel().SetDocument(bson.M{"x": 1}))
	if !b.Full() {
		t.Error("expected full for 0-capacity batch with 1 item")
	}
}

func TestBatch_Models_Slice(t *testing.T) {
	b := NewBatch(5)
	m1 := mongo.NewInsertOneModel().SetDocument(bson.M{"a": 1})
	m2 := mongo.NewInsertOneModel().SetDocument(bson.M{"b": 2})
	b.Add(m1)
	b.Add(m2)

	if len(b.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(b.Models))
	}
}
