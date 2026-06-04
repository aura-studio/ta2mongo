package gateway

import (
	"context"

	"rocket-nano/tools/tango/internal/cfgtree"
	"rocket-nano/tools/tango/internal/dao"
	"rocket-nano/tools/tango/internal/parser"
	"rocket-nano/tools/tango/internal/process"
)

// Role is the gateway runtime role (role.mode = gateway): a long-running HTTP
// server exposing the /upload log-reporting API.
type Role struct{}

// Run slices the dao / process / parser branches plus the gateway's own
// role.gateway.* section from cfg, starts the HTTP server, and serves until ctx
// is cancelled.
func (Role) Run(ctx context.Context, cfg cfgtree.Tree) error {
	daoCfg, err := dao.FromTree(cfg)
	if err != nil {
		return err
	}
	procCfg, err := process.FromTree(cfg)
	if err != nil {
		return err
	}
	parserCfg, err := parser.FromTree(cfg)
	if err != nil {
		return err
	}

	var gwCfg Config
	if err := cfg.Sub("role").Sub("gateway").Into(&gwCfg); err != nil {
		return err
	}
	gwCfg.ApplyDefaults()
	if err := gwCfg.Validate(); err != nil {
		return err
	}

	srv, err := New(ctx, daoCfg, procCfg, parserCfg.Filter, gwCfg)
	if err != nil {
		return err
	}
	defer srv.Close()

	if err := srv.EnsureIndexes(ctx); err != nil {
		return err
	}
	return srv.Run(ctx, gwCfg.Addr)
}
