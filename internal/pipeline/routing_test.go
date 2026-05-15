package pipeline

import "testing"

func TestExtractRoutingKey_AccountID(t *testing.T) {
	line := `{"#type":"user_set","#account_id":"alice","#distinct_id":"dev123","#time":"2024-01-01","#uuid":"u1"}`
	key := ExtractRoutingKey(line)
	if key != "alice" {
		t.Errorf("expected routing key 'alice', got %q", key)
	}
}

func TestExtractRoutingKey_FallbackDistinctID(t *testing.T) {
	line := `{"#type":"user_set","#distinct_id":"dev123","#time":"2024-01-01","#uuid":"u1"}`
	key := ExtractRoutingKey(line)
	if key != "dev123" {
		t.Errorf("expected routing key 'dev123', got %q", key)
	}
}

func TestExtractRoutingKey_EnvelopeFormat(t *testing.T) {
	line := `{"level":"info","msg":"{\"#type\":\"track\",\"#account_id\":\"bob\",\"#distinct_id\":\"d1\",\"#time\":\"2024-01-01\",\"#uuid\":\"u2\",\"#event_name\":\"login\"}"}`
	key := ExtractRoutingKey(line)
	if key != "bob" {
		t.Errorf("expected routing key 'bob', got %q", key)
	}
}

func TestExtractRoutingKey_EnvelopeFallbackDistinctID(t *testing.T) {
	line := `{"level":"info","message":"{\"#type\":\"track\",\"#distinct_id\":\"d2\",\"#time\":\"2024-01-01\",\"#uuid\":\"u3\",\"#event_name\":\"click\"}"}`
	key := ExtractRoutingKey(line)
	if key != "d2" {
		t.Errorf("expected routing key 'd2', got %q", key)
	}
}

func TestExtractRoutingKey_InvalidJSON(t *testing.T) {
	line := `not json at all`
	key := ExtractRoutingKey(line)
	if key != "" {
		t.Errorf("expected empty routing key for invalid JSON, got %q", key)
	}
}

func TestExtractRoutingKey_NoIdentity(t *testing.T) {
	line := `{"#type":"user_set","#time":"2024-01-01","#uuid":"u1"}`
	key := ExtractRoutingKey(line)
	if key != "" {
		t.Errorf("expected empty routing key, got %q", key)
	}
}

func TestRouteIndex_Consistency(t *testing.T) {
	// Same key always maps to same index.
	n := 4
	key := "user-abc-123"
	idx1 := RouteIndex(key, n)
	idx2 := RouteIndex(key, n)
	if idx1 != idx2 {
		t.Errorf("expected consistent routing, got %d and %d", idx1, idx2)
	}
	if idx1 < 0 || idx1 >= n {
		t.Errorf("index %d out of range [0, %d)", idx1, n)
	}
}

func TestRouteIndex_EmptyKey(t *testing.T) {
	idx := RouteIndex("", 4)
	if idx != 0 {
		t.Errorf("expected index 0 for empty key, got %d", idx)
	}
}

func TestRouteIndex_ZeroWorkers(t *testing.T) {
	idx := RouteIndex("some-key", 0)
	if idx != 0 {
		t.Errorf("expected index 0 for zero workers, got %d", idx)
	}
}

func TestRouteIndex_Distribution(t *testing.T) {
	// Verify that different keys distribute across workers.
	n := 8
	counts := make(map[int]int, n)
	keys := []string{
		"user1", "user2", "user3", "user4",
		"user5", "user6", "user7", "user8",
		"alice", "bob", "charlie", "dave",
		"eve", "frank", "grace", "heidi",
	}
	for _, k := range keys {
		counts[RouteIndex(k, n)]++
	}
	// With 16 keys and 8 buckets, at least 2 different buckets should be used.
	if len(counts) < 2 {
		t.Errorf("poor distribution: all %d keys mapped to same bucket", len(keys))
	}
}

func TestExtractRoutingKey_AccountIDPriority(t *testing.T) {
	// When both account_id and distinct_id exist, account_id wins.
	line := `{"#account_id":"acc1","#distinct_id":"dist1","#type":"user_set","#time":"2024-01-01","#uuid":"u1"}`
	key := ExtractRoutingKey(line)
	if key != "acc1" {
		t.Errorf("expected account_id 'acc1' to take priority, got %q", key)
	}
}
