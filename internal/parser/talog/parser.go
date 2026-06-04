package talog

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Parser validates and transforms raw JSON log lines into Records.
type Parser struct{}

// NewParser creates a new Parser.
func NewParser() *Parser { return &Parser{} }

// ParseLine parses a single JSON log line into a validated Record.
//
// It supports two formats:
//  1. Direct TA payload: the JSON object contains #time, #type, or #event_name at root.
//  2. Envelope format: the TA payload is a JSON string nested inside a "msg", "message", or "log" field.
//
// Returns an error if the line is not valid JSON, not a TA payload,
// or fails validation rules.
func (p *Parser) ParseLine(line string) (*Record, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Try direct TA payload first (presence of any TA root key).
	if hasTAKeys(obj) {
		return buildRecord(obj)
	}

	// Try envelope format: extract inner JSON from msg/message/log.
	if rec, err := tryParseEnvelope(obj); rec != nil || err != nil {
		return rec, err
	}

	return nil, errors.New("not a ThinkingData payload")
}

// hasTAKeys checks whether obj looks like a direct TA payload.
func hasTAKeys(obj map[string]any) bool {
	for _, key := range []string{"#time", "#type", "#event_name"} {
		if _, ok := obj[key]; ok {
			return true
		}
	}
	return false
}

// tryParseEnvelope looks for a nested JSON string in known envelope keys.
func tryParseEnvelope(obj map[string]any) (*Record, error) {
	for _, key := range EnvelopeKeys {
		s, ok := obj[key].(string)
		if !ok || len(s) < 2 || s[0] != '{' {
			continue
		}
		var inner map[string]any
		if err := json.Unmarshal([]byte(s), &inner); err != nil {
			continue
		}
		if hasTAKeys(inner) {
			return buildRecord(inner)
		}
	}
	return nil, nil
}

// buildRecord validates the TA payload and constructs a Record.
func buildRecord(payload map[string]any) (*Record, error) {
	typ := toString(payload["#type"])
	uuid := toString(payload["#uuid"])
	if typ == "" || uuid == "" {
		return nil, errors.New("#type and #uuid are required (non-empty)")
	}

	timeVal := toString(payload["#time"])
	accountID := toString(payload["#account_id"])
	distinctID := toString(payload["#distinct_id"])

	if err := validateByType(typ, timeVal, accountID, distinctID, payload); err != nil {
		return nil, err
	}

	doc := flattenPayload(payload)
	doc["#time"] = timeVal
	doc["#uuid"] = uuid
	doc["_ts"] = time.Now().UnixNano()

	return &Record{
		Type:       typ,
		UUID:       uuid,
		AccountID:  accountID,
		DistinctID: distinctID,
		Doc:        doc,
	}, nil
}

// validateByType applies TA-specific validation rules based on operation type.
func validateByType(typ, timeVal, accountID, distinctID string, payload map[string]any) error {
	hasIdentity := accountID != "" || distinctID != ""

	switch {
	case IsUserType(typ):
		if timeVal == "" {
			return errors.New("user record: #time is required")
		}
		if !hasIdentity {
			return errors.New("user record: at least one of #account_id or #distinct_id is required")
		}
	case IsEventType(typ):
		if timeVal == "" {
			return errors.New("event record: #time is required")
		}
		eventName := toString(payload["#event_name"])
		if eventName == "" {
			return errors.New("event record: #event_name is required")
		}
		if !hasIdentity {
			return errors.New("event record: at least one of #account_id or #distinct_id is required")
		}
	default:
		return fmt.Errorf("unsupported #type %q: only user_* and track* are allowed", typ)
	}
	return nil
}

// flattenPayload copies all top-level fields and promotes "properties" sub-fields
// to the root level (no key-prefix concatenation).
func flattenPayload(payload map[string]any) map[string]any {
	doc := make(map[string]any, len(payload)+4)
	for k, v := range payload {
		doc[k] = v
	}
	if props, ok := payload["properties"].(map[string]any); ok {
		for k, v := range props {
			doc[k] = v
		}
		delete(doc, "properties")
	}
	return doc
}

// toString safely converts an interface value to a string.
// Returns "" for nil or non-string values.
func toString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}
