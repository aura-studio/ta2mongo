package client

import (
	"context"
	"time"
)

func ExampleClient_Ingest() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cli, err := New(ctx, WithURI("mongodb://localhost:27017/tango"))
	if err != nil {
		return
	}
	defer cli.Close()

	_ = cli.EnsureIndexes(ctx)
	_ = cli.Ingest(ctx, `{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1","#account_id":"alice","#distinct_id":"dev123"}`)
}

func ExampleClient_IngestBatch() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := New(
		ctx,
		WithURI("mongodb://localhost:27017/tango"),
		WithMaxElapsedTime(10*time.Second),
		WithBatchSize(1000),
	)
	if err != nil {
		return
	}
	defer cli.Close()

	_ = cli.EnsureIndexes(ctx)
	_ = cli.IngestBatch(ctx, []string{
		`{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1","#account_id":"alice","#distinct_id":"dev123"}`,
		`{"#type":"track","#event_name":"click","#time":"2024-01-02","#uuid":"u2","#account_id":"alice","#distinct_id":"dev123"}`,
	})
}
