package cfgsync

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/aura-studio/tango/internal/dao"
)

// Backend is a strategy that drives config-document updates into the Watcher.
// Run blocks until ctx is cancelled (returning ctx.Err or nil) or a fatal error
// occurs, calling observe for every document it reads — both the initial
// convergence read and every subsequent update. observe applies the monotonic
// version guard and routes the document to the appliers; a non-nil error it
// returns is a bad-config signal (logged, last-good kept) and must not stop the
// backend loop.
type Backend interface {
	Run(ctx context.Context, observe func(bson.M) error) error
}

// selectBackend builds the configured backend over the given Dao. Backend
// construction never touches the connection, so an unsupported topology (e.g.
// standalone mongod with backend=changestream) surfaces a clear error from
// Run, not here.
func selectBackend(d *dao.Dao, cfg *Config) (Backend, error) {
	switch cfg.Backend {
	case BackendPoll:
		return &pollBackend{dao: d, cfg: cfg}, nil
	case BackendChangeStream:
		return &changeStreamBackend{dao: d, cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("cfgsync: unknown backend %q", cfg.Backend)
	}
}
