// Command dbctl is the consumer-side helper for the daemon log-loss rotation
// test. It talks to MongoDB with the same driver the daemon uses and offers
// three ops:
//
//	drop  — drop the target database (clean slate before a mode run)
//	count — print countDocuments on the events collection
//	wait  — poll the events count until it reaches -want, or plateaus (no change
//	        for -stable), or -timeout elapses; print the final count and verdict
//
// "wait" is how the harness decides ingestion has drained: once the count stops
// climbing it is final, and final-below-want is loss (the gap is reported).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func main() {
	var (
		uri     = flag.String("uri", "mongodb://mongo:27017", "mongo uri")
		db      = flag.String("db", "logloss", "database")
		coll    = flag.String("coll", "event", "collection")
		op      = flag.String("op", "count", "drop | count | wait")
		want    = flag.Int64("want", 0, "wait: target document count (zero-loss expectation)")
		timeout = flag.Duration("timeout", 180*time.Second, "wait: overall timeout")
		stable  = flag.Duration("stable", 12*time.Second, "wait: treat count as final after this long with no change")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+30*time.Second)
	defer cancel()

	cli, err := mongo.Connect(options.Client().ApplyURI(*uri))
	must(err)
	defer func() { _ = cli.Disconnect(context.Background()) }()
	c := cli.Database(*db).Collection(*coll)

	switch *op {
	case "drop":
		must(cli.Database(*db).Drop(ctx))
		fmt.Printf("DROP db=%s ok\n", *db)
	case "count":
		n, err := c.CountDocuments(ctx, bson.M{})
		must(err)
		fmt.Printf("COUNT %s.%s=%d\n", *db, *coll, n)
	case "wait":
		deadline := time.Now().Add(*timeout)
		var last int64 = -1
		lastChange := time.Now()
		for {
			n, err := c.CountDocuments(ctx, bson.M{})
			must(err)
			if n != last {
				last = n
				lastChange = time.Now()
			}
			reached := *want > 0 && n >= *want
			plateaued := time.Since(lastChange) >= *stable
			if reached || plateaued || time.Now().After(deadline) {
				loss := *want - n
				verdict := "LOSS"
				if loss <= 0 {
					verdict = "ZERO-LOSS"
				}
				reason := "plateau"
				if reached {
					reason = "reached-want"
				} else if time.Now().After(deadline) {
					reason = "timeout"
				}
				fmt.Printf("WAIT db=%s coll=%s want=%d got=%d loss=%d verdict=%s reason=%s\n",
					*db, *coll, *want, n, loss, verdict, reason)
				if loss > 0 {
					os.Exit(1)
				}
				return
			}
			time.Sleep(1 * time.Second)
		}
	default:
		fmt.Fprintln(os.Stderr, "dbctl: unknown -op", *op)
		os.Exit(2)
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "dbctl:", err)
		os.Exit(1)
	}
}
