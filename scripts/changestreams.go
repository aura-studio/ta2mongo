//go:build ignore

// Command changestreams enables or disables Amazon DocumentDB change streams via
// the modifyChangeStreams admin command. cfgsync's changestream backend requires
// change streams to be enabled on the target database/collection (plain MongoDB
// only needs a replica set; DocumentDB needs this command run once). This is an
// operational, run-by-hand tool — it is excluded from the normal build by the
// build:ignore tag above and is meant to be invoked with `go run`.
//
// Usage:
//
//	# Enable cluster-wide (all databases/collections) — what the integration
//	# tests need, since they use a random throwaway database each run:
//	export TANGO_TEST_MONGO_URI='mongodb://user:pass@host:27017/?tls=true&tlsCAFile=/path/global-bundle.pem&replicaSet=rs0&readPreference=primary&retryWrites=false'
//	go run scripts/changestreams.go -enable
//
//	# Scope to one database (or one collection):
//	go run scripts/changestreams.go -enable -database mydb
//	go run scripts/changestreams.go -enable -database mydb -collection _tango_config
//
//	# Turn it back off (restore original state):
//	go run scripts/changestreams.go -disable
//
// The connection string is read from -uri or, if unset, the TANGO_TEST_MONGO_URI
// environment variable. The command always runs against the primary (the admin
// command requires it).
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
		uri        = flag.String("uri", os.Getenv("TANGO_TEST_MONGO_URI"), "Mongo/DocumentDB connection string (default: $TANGO_TEST_MONGO_URI)")
		enable     = flag.Bool("enable", false, "enable change streams")
		disable    = flag.Bool("disable", false, "disable change streams")
		database   = flag.String("database", "", `target database ("" = all databases)`)
		collection = flag.String("collection", "", `target collection ("" = all collections)`)
	)
	flag.Parse()

	if *enable == *disable { // both false or both true
		fmt.Fprintln(os.Stderr, "error: pass exactly one of -enable or -disable")
		flag.Usage()
		os.Exit(2)
	}
	if *uri == "" {
		fmt.Fprintln(os.Stderr, "error: no connection string (-uri or $TANGO_TEST_MONGO_URI)")
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := mongo.Connect(options.Client().ApplyURI(*uri))
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	var res bson.M
	err = client.Database("admin").RunCommand(ctx, bson.D{
		{Key: "modifyChangeStreams", Value: 1},
		{Key: "database", Value: *database},
		{Key: "collection", Value: *collection},
		{Key: "enable", Value: *enable},
	}).Decode(&res)
	if err != nil {
		fmt.Fprintln(os.Stderr, "modifyChangeStreams:", err)
		os.Exit(1)
	}

	action := "enabled"
	if *disable {
		action = "disabled"
	}
	scope := "cluster-wide"
	if *database != "" {
		scope = *database
		if *collection != "" {
			scope += "." + *collection
		}
	}
	fmt.Printf("change streams %s (%s): %v\n", action, scope, res)
}
