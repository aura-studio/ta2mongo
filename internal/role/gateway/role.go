package gateway

import (
	"context"

	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/logging"
)

// Role is the gateway runtime role (role.mode = gateway): a long-running HTTP
// server exposing the /upload log-reporting API.
type Role struct{}

// Run builds the server from cfg (see NewFromTree for the per-module config
// slicing), starts the HTTP server, and serves until ctx is cancelled.
//
// Degraded unavailable mode: when dao.mongo.uri is intentionally left empty the
// gateway still starts and listens — /healthz stays 200 so the orchestrator
// keeps the pod — but every API route answers 503 "uri is empty, service
// unavailable" instead of touching a database. Typed New/NewFromTree are
// unchanged (library callers still get an error for an empty URI); this is a
// role-level policy only.
func (Role) Run(ctx context.Context, cfg cfgtree.Tree) error {
	if !dao.URIConfigured(cfg) {
		gwCfg, err := configFromTree(cfg)
		if err != nil {
			return err
		}
		logging.Warn("tango gateway: dao.mongo.uri is empty — serving in unavailable mode: " +
			"/healthz stays 200, API routes answer 503 \"" + errServiceUnavailable.Error() + "\"; " +
			"set dao.mongo.uri (or TANGO_DAO_MONGO_URI) and restart to enable")
		srv := &Server{} // nil engine → unavailable mode (see Handler)
		return srv.Run(ctx, gwCfg.Addr)
	}

	srv, gwCfg, err := NewFromTree(ctx, cfg)
	if err != nil {
		return err
	}
	defer srv.Close()

	if err := srv.EnsureIndexes(ctx); err != nil {
		return err
	}
	return srv.Run(ctx, gwCfg.Addr)
}
