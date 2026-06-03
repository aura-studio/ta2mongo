package remoteconfig

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/core/runtime"
)

// ApplyAtStartup performs a one-shot remote-config merge that every run mode
// uses before constructing its pipeline. When RemoteConfig.Enabled is false it
// returns cfg unchanged. Otherwise it opens a short-lived MongoDB connection
// (using the local mongoURI, which is never remotely overridable), fetches the
// override document, merges it per-field, re-validates, and returns the result.
//
// A missing document is not an error — the local config is used as-is.
func ApplyAtStartup(ctx context.Context, cfg config.Config, logger *logrus.Logger) (config.Config, error) {
	if !cfg.RemoteConfig.Enabled {
		return cfg, nil
	}

	res, err := runtime.ConnectMongo(ctx, cfg.Mongo)
	if err != nil {
		return cfg, fmt.Errorf("remoteconfig: %w", err)
	}
	defer func() { _ = res.Close() }()

	coll := res.DB.Collection(cfg.RemoteConfig.Collection)

	doc, err := Fetch(ctx, coll, cfg.RemoteConfig.DocumentID)
	if err != nil {
		return cfg, err
	}
	if doc == nil {
		logger.WithFields(logrus.Fields{
			"collection": cfg.RemoteConfig.Collection,
			"documentID": cfg.RemoteConfig.DocumentID,
		}).Warn("remoteconfig: enabled but no config document found; using local config")
		return cfg, nil
	}

	merged, err := Merge(cfg, doc)
	if err != nil {
		return cfg, err
	}
	if err := merged.Validate(); err != nil {
		return cfg, fmt.Errorf("remoteconfig: merged config invalid: %w", err)
	}

	logger.WithFields(logrus.Fields{
		"collection":     cfg.RemoteConfig.Collection,
		"documentID":     cfg.RemoteConfig.DocumentID,
		"override_keys":  keysOf(doc),
		"filter_include": merged.Filter.Include,
		"filter_exclude": merged.Filter.Exclude,
	}).Info("remoteconfig: applied remote override")

	return merged, nil
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
