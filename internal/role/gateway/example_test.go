package gateway

import (
	"context"
	"time"

	"rocket-nano/tools/tango/internal/dao"
	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/process"
)

func ExampleServer_Upload() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	procCfg := &process.Config{Mode: string(process.ModeBatch)}
	srv, err := New(ctx, &dao.Config{Mongo: &daomongo.Config{URI: "mongodb://localhost:27017/tango"}}, procCfg, nil, Config{})
	if err != nil {
		return
	}
	defer srv.Close()

	_ = srv.EnsureIndexes(ctx)

	// process.mode=batch: an array of lines, flushed in bulk.
	_, _ = srv.Upload(ctx, []string{
		`{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1","#account_id":"alice","#distinct_id":"dev123"}`,
		`{"#type":"track","#event_name":"click","#time":"2024-01-02","#uuid":"u2","#account_id":"alice","#distinct_id":"dev123"}`,
	})
}
