// Package role is the runtime-role collection. Each role has its own package
// under internal/role/<name> (api, cli, daemon, gateway); role.Config (config.go)
// aggregates the per-role file configs. Roles never re-host shared module configs
// (process / parser / source) — those stay at their own top-level package paths
// and are reused by whichever role needs them.
package role

// Canonical runtime-role identifiers (also the cobra command names).
const (
	API     = "api"
	CLI     = "cli"
	Daemon  = "daemon"
	Gateway = "gateway"
)
