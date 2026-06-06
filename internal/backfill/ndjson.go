package backfill

import (
	"encoding/json"
	"fmt"
	"io"
)

// streamResultPageBody consumes an NDJSON result-page body and yields each
// row through onRow. An envelope-shaped JSON object on the first token is
// treated as an API error (typical for "task expired" responses returned
// with HTTP 200).
func streamResultPageBody(body io.Reader, onRow func(row []interface{}) error) error {
	dec := json.NewDecoder(body)
	dec.UseNumber()

	var rowsSeen int
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("decode result-page: %w", err)
		}
		if d, ok := tok.(json.Delim); ok && d == '{' {
			obj, err := readObjectRemainder(dec)
			if err != nil {
				return fmt.Errorf("decode envelope error: %w", err)
			}
			env := envelope{}
			if rc, ok := obj["return_code"]; ok {
				switch v := rc.(type) {
				case json.Number:
					n, _ := v.Int64()
					env.ReturnCode = int(n)
				case float64:
					env.ReturnCode = int(v)
				}
			}
			if rm, ok := obj["return_message"].(string); ok {
				env.ReturnMessage = rm
			}
			if env.ReturnCode != 0 {
				apiErr := &APIError{Code: env.ReturnCode, Message: env.ReturnMessage}
				if isExpired(apiErr) {
					return ErrTaskExpired
				}
				return apiErr
			}
			continue
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return fmt.Errorf("decode result-page: unexpected token %v", tok)
		}
		row, err := readArrayRemainder(dec)
		if err != nil {
			return fmt.Errorf("decode row %d: %w", rowsSeen, err)
		}
		if err := onRow(row); err != nil {
			return err
		}
		rowsSeen++
	}
	return nil
}

// readObjectRemainder reads JSON object members until the closing '}',
// returning a generic map. The decoder must have already consumed the
// opening '{'.
func readObjectRemainder(dec *json.Decoder) (map[string]any, error) {
	out := make(map[string]any)
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("expected string key, got %T", keyTok)
		}
		var val any
		if err := dec.Decode(&val); err != nil {
			return nil, err
		}
		out[key] = val
	}
	// Consume the closing '}'.
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}

// readArrayRemainder reads JSON array elements until the closing ']'. The
// decoder must have already consumed the opening '['.
func readArrayRemainder(dec *json.Decoder) ([]interface{}, error) {
	var out []interface{}
	for dec.More() {
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return out, nil
}
