package shared

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"rocket-nano/tools/tango/config"
	"rocket-nano/tools/tango/internal/log"
	"rocket-nano/tools/tango/internal/service/report"
)

// RunStandaloneService loads the standalone config from path and runs the
// report service (tail TA logs -> filter -> MongoDB).
func RunStandaloneService(cmd *cobra.Command, path string) error {
	_, rt, err := config.LoadStandalone(path, cmd.Flags())
	if err != nil {
		return err
	}
	log.Init(rt.Logging.Level)
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	log.WithFields(log.Fields{
		"pid":       os.Getpid(),
		"go_procs":  runtime.GOMAXPROCS(0),
		"mongo_uri": MaskURI(rt.Mongo.URI),
		"role":      "standalone",
	}).Info("tango standalone: starting")

	return runReport(ctx, rt)
}

// runReport runs the reporting pipeline and blocks until ctx is cancelled.
func runReport(ctx context.Context, rt config.Config) error {
	svc, err := report.New(ctx, rt)
	if err != nil {
		log.WithError(err).Error("tango standalone: init failed")
		return err
	}
	defer func() {
		if err := svc.Shutdown(); err != nil {
			log.WithError(err).Error("tango standalone: shutdown error")
		}
	}()

	if err := svc.EnsureIndexes(ctx); err != nil {
		log.WithError(err).Error("tango standalone: ensure indexes failed")
		return err
	}

	return svc.Run(ctx)
}

// MaskURI masks the credentials portion of a MongoDB URI for safe logging.
func MaskURI(uri string) string {
	i := strings.Index(uri, "://")
	if i < 0 {
		return uri
	}
	rest := uri[i+3:]
	at := strings.IndexByte(rest, '@')
	if at < 0 {
		return uri
	}
	return fmt.Sprintf("%s***:***@%s", uri[:i+3], rest[at+1:])
}
