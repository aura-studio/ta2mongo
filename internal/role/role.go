// Package role is the runtime-role collection. Each runnable role has its own
// package under internal/role/<name> (cli, daemon, gateway) and implements the
// Role interface; api is the embeddable ingestion engine the roles build on, not
// a mode-dispatched role. role.Config (config.go) aggregates the role.* file
// keys (mode plus the per-role sections). Roles never re-host shared module
// configs (dao / parser / source / process) — those stay at their own top-level
// package paths and each role slices the ones it needs from the config tree.
package role

import (
	"context"
	"fmt"

	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/role/cli"
	"github.com/aura-studio/tango/internal/role/daemon"
	"github.com/aura-studio/tango/internal/role/gateway"
)

// Canonical runtime-role identifiers (also the config role.mode values).
const (
	API     = "api"
	CLI     = "cli"
	Daemon  = "daemon"
	Gateway = "gateway"
)

// Role is the uniform runtime contract every runnable role implements: run until
// done (or until ctx is cancelled), slicing whatever config branches it needs
// from the supplied tree. The caller selects the implementation by role.mode via
// Get and hands it the whole tree.
type Role interface {
	Run(ctx context.Context, cfg cfgtree.Tree) error
}

// Get returns the Role implementation for mode (one of daemon / gateway / cli).
// An unknown or non-runnable mode (e.g. api) is an error.
func Get(mode string) (Role, error) {
	switch mode {
	case Daemon:
		return daemon.Role{}, nil
	case Gateway:
		return gateway.Role{}, nil
	case CLI:
		return cli.Role{}, nil
	default:
		return nil, fmt.Errorf("role.mode %q is not runnable (want %q / %q / %q)",
			mode, Daemon, Gateway, CLI)
	}
}
