package pipeline

import "go.mongodb.org/mongo-driver/v2/mongo"

// Batch accumulates write models for a single collection.
type Batch struct {
	Models []mongo.WriteModel
	cap    int
}

// NewBatch creates a Batch with the given capacity hint.
func NewBatch(capacity int) *Batch {
	return &Batch{
		Models: make([]mongo.WriteModel, 0, capacity),
		cap:    capacity,
	}
}

// Add appends a write model to the batch.
func (b *Batch) Add(m mongo.WriteModel) { b.Models = append(b.Models, m) }

// Full reports whether the batch has reached its capacity.
func (b *Batch) Full() bool { return len(b.Models) >= b.cap }

// Empty reports whether the batch contains no models.
func (b *Batch) Empty() bool { return len(b.Models) == 0 }

// Len returns the current number of models in the batch.
func (b *Batch) Len() int { return len(b.Models) }

// Reset clears the batch for reuse, keeping the underlying array.
func (b *Batch) Reset() {
	b.Models = b.Models[:0]
}
