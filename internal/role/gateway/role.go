package gateway

import (
	"context"

	"github.com/aura-studio/tango/internal/cfgtree"
)

// Role is the gateway runtime role (role.mode = gateway): a long-running HTTP
// server exposing the /upload log-reporting API.
type Role struct{}

// Run builds the server from cfg (see NewFromTree for the per-module config
// slicing), starts the HTTP server, and serves until ctx is cancelled.
func (Role) Run(ctx context.Context, cfg cfgtree.Tree) error {
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
