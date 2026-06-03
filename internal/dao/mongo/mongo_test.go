package mongo

import "testing"

// TestMongoResourceClose_NilReceiver verifies Close is a safe no-op for nil
// receivers and resources with no client attached.
func TestMongoResourceClose_NilReceiver(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var r *MongoResource
		if err := r.Close(); err != nil {
			t.Errorf("nil Close = %v, want nil", err)
		}
	})
	t.Run("nil client is guarded", func(t *testing.T) {
		r := &MongoResource{}
		if err := r.Close(); err != nil {
			t.Errorf("nil client Close = %v, want nil", err)
		}
	})
}
