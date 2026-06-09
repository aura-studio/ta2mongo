// Command perf is a tiny throughput stress test for the tango daemon's report
// pipeline (tail -> parse -> filter -> identity -> Mongo/DocumentDB bulk write).
// It pre-fills a log file with -n TA track events, runs the real daemon Service
// against TANGO_TEST_MONGO_URI, and reports how fast they land in the database.
//
// Intended to run inside the test environment (e.g. on the EC2 host, in-VPC next
// to DocumentDB, where replica-set discovery works):
//
//	export TANGO_TEST_MONGO_URI='mongodb://user:pass@<docdb>:27017/?tls=true&tlsCAFile=./global-bundle.pem&replicaSet=rs0&readPreference=primary&retryWrites=false'
//	./perf -n 50000 -workers 4 -batch 1000
//
// It writes to a throwaway database (tango_perf_<unixsec>) and drops it on exit.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/aura-studio/tango/internal/dao"
	daomongo "github.com/aura-studio/tango/internal/dao/mongo"
	"github.com/aura-studio/tango/internal/logging"
	"github.com/aura-studio/tango/internal/parser"
	"github.com/aura-studio/tango/internal/process"
	"github.com/aura-studio/tango/internal/process/pipeline"
	"github.com/aura-studio/tango/internal/role/daemon"
	"github.com/aura-studio/tango/internal/source"
	"github.com/aura-studio/tango/internal/source/tailer"
)

func main() {
	n := flag.Int("n", 50000, "number of TA track events to report")
	workers := flag.Int("workers", 4, "pipeline batch workers")
	batch := flag.Int("batch", 1000, "batch size")
	users := flag.Int("users", 500, "distinct account_ids (identity cache pressure)")
	flag.Parse()
	logging.Init(&logging.Config{Level: "warn"})

	uri := os.Getenv("TANGO_TEST_MONGO_URI")
	if uri == "" {
		fmt.Fprintln(os.Stderr, "perf: set TANGO_TEST_MONGO_URI")
		os.Exit(2)
	}
	dbName := fmt.Sprintf("tango_perf_%d", time.Now().Unix())
	dbURI := withDB(uri, dbName)

	// Verify handle for counting + cleanup.
	vc, err := mongo.Connect(options.Client().ApplyURI(dbURI))
	must(err)
	db := vc.Database(dbName)
	defer func() {
		dctx, c := context.WithTimeout(context.Background(), 30*time.Second)
		defer c()
		_ = db.Drop(dctx)
		_ = vc.Disconnect(dctx)
	}()

	// Pre-fill a log file with n track events (bounded account set so identity
	// resolution is mostly cache hits — we measure the event write path).
	dir, err := os.MkdirTemp("", "tango-perf-")
	must(err)
	defer os.RemoveAll(dir)
	logPath := filepath.Join(dir, "ta.log")
	bytesIn := writeEvents(logPath, *n, *users)

	srcCfg := &source.Config{Tailer: &tailer.Config{
		LogPattern:     []string{filepath.Join(dir, "*.log")},
		TailMode:       tailer.TailModePoll,
		RescanInterval: 200 * time.Millisecond,
		PollInterval:   20 * time.Millisecond,
	}}
	srcCfg.ApplyDefaults()
	procCfg := &process.Config{Pipeline: &pipeline.Config{
		BatchWorkers: *workers, BatchSize: *batch, FlushInterval: 200 * time.Millisecond,
	}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc, err := daemon.New(ctx, &dao.Config{Mongo: &daomongo.Config{URI: dbURI}}, &parser.Config{}, srcCfg, procCfg, nil)
	must(err)
	defer svc.Shutdown()
	must(svc.EnsureIndexes(ctx))

	fmt.Printf("perf: engine=%s db=%s n=%d workers=%d batch=%d users=%d bytes_in=%.1fMB\n",
		maskURI(uri), dbName, *n, *workers, *batch, *users, float64(bytesIn)/(1<<20))

	start := time.Now()
	go func() { _ = svc.Run(ctx) }()

	var got int64
	deadline := start.Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		got = countEvents(db)
		if got >= int64(*n) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	elapsed := time.Since(start)
	cancel()

	eps := float64(got) / elapsed.Seconds()
	mbps := float64(bytesIn) / (1 << 20) / elapsed.Seconds()
	fmt.Printf("perf: ingested=%d/%d  elapsed=%s  throughput=%.0f events/s  %.2f MB/s\n",
		got, *n, elapsed.Round(time.Millisecond), eps, mbps)
	if got < int64(*n) {
		fmt.Printf("perf: WARN only %d/%d landed before the 15min deadline\n", got, *n)
		os.Exit(1)
	}
}

func writeEvents(path string, n, users int) int64 {
	f, err := os.Create(path)
	must(err)
	defer f.Close()
	var total int64
	for i := 0; i < n; i++ {
		line := fmt.Sprintf(`{"#type":"track","#event_name":"PaymentOrderState","#time":"2026-06-09 12:00:00.000","#uuid":"perf-%d","#account_id":"u%d","properties":{"seq":%d,"amount":12.34,"state":"paid"}}`+"\n",
			i, i%users, i)
		w, err := f.WriteString(line)
		must(err)
		total += int64(w)
	}
	return total
}

func countEvents(db *mongo.Database) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := db.Collection("event").CountDocuments(ctx, bson.M{})
	if err != nil {
		return 0
	}
	return c
}

// withDB injects the database into a Mongo URI before any query string.
func withDB(uri, db string) string {
	base, query := uri, ""
	if i := strings.IndexByte(uri, '?'); i >= 0 {
		base, query = uri[:i], uri[i:]
	}
	return strings.TrimRight(base, "/") + "/" + db + query
}

func maskURI(uri string) string {
	i := strings.Index(uri, "://")
	if i < 0 {
		return uri
	}
	rest := uri[i+3:]
	at := strings.IndexByte(rest, '@')
	if at < 0 {
		return uri
	}
	return uri[:i+3] + "***:***@" + rest[at+1:]
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "perf:", err)
		os.Exit(2)
	}
}
