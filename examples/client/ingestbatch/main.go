package main

import (
	"context"
	"flag"
	"log"
	"time"

	"rocket-nano/tools/tango/client"
)

func main() {
	var (
		uri       = flag.String("uri", "mongodb://localhost:27017/tango", "mongodb URI (must include db name in path)")
		maxRetry  = flag.Duration("maxElapsedTime", 10*time.Second, "mongo retry max elapsed time")
		batchSize = flag.Int("batchSize", 1000, "client batch size (used by IngestBatch)")
		timeout   = flag.Duration("timeout", 30*time.Second, "operation timeout")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cli, err := client.New(
		ctx,
		client.WithURI(*uri),
		client.WithMaxElapsedTime(*maxRetry),
		client.WithBatchSize(*batchSize),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = cli.Close() }()

	if err := cli.EnsureIndexes(ctx); err != nil {
		log.Fatal(err)
	}

	lines := []string{
		`{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1","#account_id":"alice","#distinct_id":"dev123"}`,
		`{"#type":"track","#event_name":"click","#time":"2024-01-02","#uuid":"u2","#account_id":"alice","#distinct_id":"dev123"}`,
	}

	if err := cli.IngestBatch(ctx, lines); err != nil {
		log.Fatal(err)
	}

	log.Println("ingest batch ok")
}
