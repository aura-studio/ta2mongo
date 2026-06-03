package worker

import (
	"github.com/go-viper/mapstructure/v2"

	"rocket-nano/tools/tango/config"
)

// reportSyncFilters extracts include/exclude expression lists from a report-sync
// payload, accepting either top-level filterInclude/filterExclude arrays or a
// nested filter:{include,exclude} object (the remote-config document shape).
func reportSyncFilters(payload map[string]any) (include, exclude []string) {
	if f, ok := payload["filter"].(map[string]any); ok {
		return toStringSlice(f["include"]), toStringSlice(f["exclude"])
	}
	return toStringSlice(payload["filterInclude"]), toStringSlice(payload["filterExclude"])
}

// decodePayload maps the task payload onto a struct via mapstructure with the
// duration/slice hooks, leaving unset fields at their incoming value.
func decodePayload(payload map[string]any, target any) error {
	if len(payload) == 0 {
		return nil
	}
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           target,
		TagName:          "mapstructure",
		WeaklyTypedInput: true,
		DecodeHook: mapstructure.ComposeDecodeHookFunc(
			mapstructure.StringToTimeDurationHookFunc(),
			mapstructure.StringToSliceHookFunc(","),
		),
	})
	if err != nil {
		return err
	}
	return dec.Decode(payload)
}

// overlayBackfillFilter populates the backfill selection filter from a task
// payload. It accepts a nested backfillFilter:{table,events,include,exclude}
// object and/or top-level conveniences (table, events, filterInclude,
// filterExclude). Backfill never uses the reporting filter, so nothing here
// touches cfg.Filter.
func overlayBackfillFilter(payload map[string]any, cfg *config.Config) error {
	if bf, ok := payload["backfillFilter"].(map[string]any); ok {
		if err := decodePayload(bf, &cfg.BackfillFilter); err != nil {
			return err
		}
	}
	if v, ok := payload["table"].(string); ok && v != "" {
		cfg.BackfillFilter.Table = v
	}
	if v, ok := payload["events"]; ok {
		cfg.BackfillFilter.Events = toStringSlice(v)
	}
	if v, ok := payload["filterInclude"]; ok {
		cfg.BackfillFilter.Include = toStringSlice(v)
	}
	if v, ok := payload["filterExclude"]; ok {
		cfg.BackfillFilter.Exclude = toStringSlice(v)
	}
	return nil
}

func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	default:
		return nil
	}
}
