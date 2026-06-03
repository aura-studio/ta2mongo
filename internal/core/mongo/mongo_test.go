package mongo

import "testing"

// TestMongoResourceClose_Ownership verifies Close only disconnects an owned
// connection and is a safe no-op for borrowed resources and nil receivers — the
// guarantee that lets NewFromClient callers keep ownership of their client.
func TestMongoResourceClose_Ownership(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var r *MongoResource
		if err := r.Close(); err != nil {
			t.Errorf("nil Close = %v, want nil", err)
		}
	})
	t.Run("borrowed is a no-op", func(t *testing.T) {
		// Owns=false with a nil client: Close must not touch the client.
		r := &MongoResource{Owns: false}
		if err := r.Close(); err != nil {
			t.Errorf("borrowed Close = %v, want nil", err)
		}
	})
	t.Run("owned with nil client is guarded", func(t *testing.T) {
		r := &MongoResource{Owns: true}
		if err := r.Close(); err != nil {
			t.Errorf("owned(nil client) Close = %v, want nil", err)
		}
	})
}
