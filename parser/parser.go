package parser

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/aura-studio/cast"
)

type TaRecord struct {
	Type    string
	UUID    string
	Payload map[string]any
	Doc     map[string]any
}

type Parser struct{}

func NewParser() *Parser {
	return &Parser{}
}

func (p *Parser) ParseLine(line string) (*TaRecord, error) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(line), &obj); err != nil {
		return nil, err
	}

	// thinkingdata payload should have these keys
	if _, ok := obj["#time"]; ok {
		return p.parsePayload(obj)
	}
	if _, ok := obj["#type"]; ok {
		return p.parsePayload(obj)
	}
	if _, ok := obj["#event_name"]; ok {
		return p.parsePayload(obj)
	}

	// Heuristic envelope: msg/message/log contains json string
	for _, k := range []string{"msg", "message", "log"} {
		if v, ok := obj[k]; ok {
			if s, ok := v.(string); ok {
				s = cast.ToString(s)
				if len(s) == 0 {
					continue
				}
				// quick check: likely inner json
				if len(s) >= 2 && s[0] == '{' {
					var inner map[string]any
					if err := json.Unmarshal([]byte(s), &inner); err == nil {
						if _, ok := inner["#time"]; ok {
							return p.parsePayload(inner)
						}
						if _, ok := inner["#type"]; ok {
							return p.parsePayload(inner)
						}
					}
				}
			}
		}
	}

	return nil, errors.New("not a thinkingdata payload")
}

func isUserType(typeName string) bool {
	return strings.HasPrefix(typeName, "user_")
}

func isEventType(typeName string) bool {
	// ThinkingData track types are usually: track/track_update/track_overwrite
	return strings.HasPrefix(typeName, "track")
}

func (p *Parser) parsePayload(payload map[string]any) (*TaRecord, error) {
	// keep validated requirements based on original payload
	typ := cast.ToString(payload["#type"])
	uuid := cast.ToString(payload["#uuid"])
	if typ == "" || uuid == "" {
		// non-null and not empty string required
		return nil, errors.New("missing #type or #uuid (non-null and non-empty required)")
	}

	timeVal := cast.ToString(payload["#time"])
	accountID := cast.ToString(payload["#account_id"])
	distinctID := cast.ToString(payload["#distinct_id"])
	eventName := cast.ToString(payload["#event_name"])

	// existence: non-null and not empty string required
	if isUserType(typ) {
		// user: #type + #time + #uuid required
		if timeVal == "" {
			return nil, errors.New("user: #time is required (non-null and non-empty)")
		}
		if accountID == "" && distinctID == "" {
			return nil, errors.New("user: require one of #account_id/#distinct_id (non-null and non-empty)")
		}
	} else if isEventType(typ) {
		// event: #type + #time + #event_name + #uuid required
		if timeVal == "" {
			return nil, errors.New("event: #time is required (non-null and non-empty)")
		}
		if eventName == "" {
			return nil, errors.New("event: #event_name is required (non-null and non-empty)")
		}
		if accountID == "" && distinctID == "" {
			return nil, errors.New("event: require one of #account_id/#distinct_id (non-null and non-empty)")
		}
	} else {
		// if #type is neither event nor user, go to dead_letter
		return nil, errors.New("unsupported #type; only user_* and track_* are allowed")
	}

	// 1) put entire json (original payload) "as-is" into Payload
	// 2) build Doc by flattening fields to outer layer
	doc := make(map[string]any, len(payload)+3)
	for k, v := range payload {
		doc[k] = v
	}

	// thinkingdata formatter puts body fields into "properties".
	// Flatten "properties" into the root (no parent-name prefix concatenation).
	if props, ok := payload["properties"].(map[string]any); ok {
		for k, v := range props {
			doc[k] = v
		}
		delete(doc, "properties")
	}

	// Ensure validated #time and #uuid exist as non-empty strings
	doc["#time"] = timeVal
	doc["#uuid"] = uuid

	// Ensure doc also carries _ts (ns) for troubleshooting/auditing
	doc["_ts"] = time.Now().UnixNano()

	return &TaRecord{
		Type:    typ,
		UUID:    uuid,
		Payload: payload,
		Doc:     doc,
	}, nil
}
