package backfill

import (
	"errors"
	"strings"
	"testing"
)

// TestStreamResultPageBody_Rows parses an NDJSON page of array rows.
func TestStreamResultPageBody_Rows(t *testing.T) {
	body := `["track","login"]
["track","pay"]
["user_set","u-1"]`
	var got [][]interface{}
	err := streamResultPageBody(strings.NewReader(body), func(row []interface{}) error {
		got = append(got, row)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3", len(got))
	}
	if got[0][1] != "login" || got[2][0] != "user_set" {
		t.Errorf("unexpected rows: %v", got)
	}
}

// TestStreamResultPageBody_EnvelopeError surfaces an envelope object as an
// APIError, and a task-expired message as ErrTaskExpired.
func TestStreamResultPageBody_EnvelopeError(t *testing.T) {
	t.Run("api_error", func(t *testing.T) {
		body := `{"return_code": 100, "return_message": "boom"}`
		err := streamResultPageBody(strings.NewReader(body), func([]interface{}) error { return nil })
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != 100 {
			t.Fatalf("err = %v, want *APIError code=100", err)
		}
	})

	t.Run("expired", func(t *testing.T) {
		body := `{"return_code": 1, "return_message": "task not found"}`
		err := streamResultPageBody(strings.NewReader(body), func([]interface{}) error { return nil })
		if !errors.Is(err, ErrTaskExpired) {
			t.Fatalf("err = %v, want ErrTaskExpired", err)
		}
	})

	t.Run("zero_code_ignored", func(t *testing.T) {
		// A leading envelope with return_code 0 is skipped; rows still flow.
		body := `{"return_code": 0, "return_message": "ok"}
["track","x"]`
		n := 0
		err := streamResultPageBody(strings.NewReader(body), func([]interface{}) error { n++; return nil })
		if err != nil || n != 1 {
			t.Fatalf("err=%v rows=%d, want nil/1", err, n)
		}
	})
}

// TestStreamResultPageBody_OnRowError aborts on a non-nil onRow return.
func TestStreamResultPageBody_OnRowError(t *testing.T) {
	sentinel := errors.New("stop")
	err := streamResultPageBody(strings.NewReader("[\"a\"]\n[\"b\"]"), func([]interface{}) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want sentinel", err)
	}
}
