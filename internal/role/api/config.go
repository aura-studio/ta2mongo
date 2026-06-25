// config.go — re-exports of the module config types New's own signature
// takes, plus the resolved config tree NewFromTree consumes. Embedders
// (notably the public client package) hold and populate these while depending
// on the api package alone: the client is the engine's thin public face and
// must not import the internal config-owning domains (dao / parser / process /
// cfgsync / cfgtree) directly — the same layering the engine's
// bytes-in/bytes-out query faces (EJSONBytes / SQLBytes) enforce for the dao
// request/response types.
package api

import (
	"github.com/aura-studio/tango/internal/backfill"
	"github.com/aura-studio/tango/internal/cfgsync"
	"github.com/aura-studio/tango/internal/cfgtree"
	"github.com/aura-studio/tango/internal/dao"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/source"
)

// DaoConfig is the dao.* module config (dao.mongo.* + dao.store.*). Alias for
// dao.Config, so a value populated by an embedder is handed to New as-is.
type DaoConfig = dao.Config

// ParserConfig is the parser.* module config (parser.filter.*). Alias for
// parser.Config.
type ParserConfig = parser.Config

// ProcessConfig is the process.* module config (process.* +
// process.pipeline.*). Alias for process.Config.
type ProcessConfig = process.Config

// CfgsyncConfig is the cfgsync.* module config (central-config collection /
// document id / watcher settings). Alias for cfgsync.Config.
type CfgsyncConfig = cfgsync.Config

// FileConfig is the source.file.* module config (the one-shot file-import
// source's explicit paths and line cap). Alias for the source facade's
// FileConfig (itself an alias for file.Config — DAO-6: the role layer names
// source types through the facade only), so an embedder hands a populated value
// to Engine.File without importing the source domain.
type FileConfig = source.FileConfig

// BackfillConfig is the backfill.* module config (TA OpenAPI history backfill).
// Alias for backfill.Config, so an embedder (notably the public client) hands a
// populated value to Engine.RunBackfill while depending on the api package
// alone.
type BackfillConfig = backfill.Config

// MemConfig is the source.mem.* module config (the in-memory relay source's
// buffer size). Alias for the source facade's MemConfig (itself an alias for
// mem.Config — DAO-6: the role layer names source types through the facade
// only). Engine.RunBackfill sizes the relay it feeds fetched rows through from
// it; a nil value uses the relay's default buffer.
type MemConfig = source.MemConfig

// Tree is the resolved unified-config tree NewFromTree slices its module
// branches from. Alias for cfgtree.Tree.
type Tree = cfgtree.Tree
