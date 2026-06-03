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
		uri           = flag.String("uri", "mongodb://localhost:27017/tango", "mongodb URI (must include db name in path)")
		line          = flag.String("line", `{"#type":"track","#event_name":"login","#time":"2024-01-01","#uuid":"u1","#account_id":"alice","#distinct_id":"dev123"}`, "single ThinkingData JSON line")
		operationTime = flag.Duration("timeout", 15*time.Second, "operation timeout")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *operationTime)
	defer cancel()

	cli, err := client.New(
		ctx,
		client.WithURI(*uri),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		_ = cli.Close()
	}()

	if err := cli.EnsureIndexes(ctx); err != nil {
		log.Fatal(err)
	}

	if err := cli.Ingest(ctx, *line); err != nil {
		log.Fatal(err)
	}

	log.Println("ingest ok")
}
