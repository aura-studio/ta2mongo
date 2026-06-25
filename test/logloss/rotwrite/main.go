// Command rotwrite drives the daemon log-loss rotation test. It writes a fixed
// number of unique ThinkingData track lines into a size-rotated set of files
// named "log.<timestamp>", keeping at most -keep files (the oldest is deleted
// when a new one pushes the count over the limit) — i.e. the production rotation
// the user runs (log.<ts>, fixed size per file, max 5 kept). Each line carries a
// globally-unique #uuid (rw-<seq>) so the consumer side can verify exact,
// loss-free ingestion by counting distinct events.
//
// Writes are paced to a target MB/s so the file set churns the way a streaming
// log does in production: a file lives for ~keep rotations before deletion,
// giving the tailer time to read it. Writing the whole set instantly would
// delete the oldest files before any tailer could discover them — that models
// nothing real, so pacing is mandatory for a meaningful loss measurement.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func main() {
	var (
		dir     = flag.String("dir", "", "log directory (required)")
		sizeMB  = flag.Int("size", 10, "per-file size cap in MB (rotate when exceeded)")
		keep    = flag.Int("keep", 5, "max number of log.* files kept (oldest deleted beyond this)")
		lines   = flag.Int("lines", 480000, "total lines to write")
		rateMBs = flag.Float64("rate", 8, "write pace in MB/s (streaming simulation)")
	)
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "rotwrite: -dir is required")
		os.Exit(2)
	}
	must(os.MkdirAll(*dir, 0o755))

	sizeBytes := int64(*sizeMB) * 1024 * 1024
	bytesPerSec := *rateMBs * 1024 * 1024

	var (
		live         []string // created files, oldest first
		filesCreated int
		filesDeleted int
		bytesTotal   int64
		curFile      *os.File
		curBytes     int64
		lastStamp    int64
	)

	// newFile creates the next log.<timestamp> file and trims the live set to
	// -keep by deleting the oldest, exactly like a size+backups rotator.
	newFile := func() {
		if curFile != nil {
			_ = curFile.Close()
		}
		// Monotonic nanosecond stamp; bump if two rotations land in the same ns
		// so names stay unique and sortable.
		stamp := time.Now().UnixNano()
		if stamp <= lastStamp {
			stamp = lastStamp + 1
		}
		lastStamp = stamp
		path := filepath.Join(*dir, "log."+strconv.FormatInt(stamp, 10))
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		must(err)
		curFile = f
		curBytes = 0
		filesCreated++
		live = append(live, path)
		for len(live) > *keep {
			oldest := live[0]
			live = live[1:]
			if err := os.Remove(oldest); err == nil {
				filesDeleted++
			}
		}
	}

	newFile()
	start := time.Now()
	for seq := 0; seq < *lines; seq++ {
		line := taLine(seq)
		if curBytes+int64(len(line)) > sizeBytes {
			newFile()
		}
		// Write each complete line directly (no user-space buffering), so a tailer
		// reading the file concurrently never observes a partial line at a buffer
		// boundary — keeps the zero-loss criterion exactly count == lines.
		n, err := curFile.WriteString(line)
		must(err)
		curBytes += int64(n)
		bytesTotal += int64(n)

		// Pace to the target byte rate (compute in float ns to avoid truncation).
		ideal := time.Duration(float64(bytesTotal) / bytesPerSec * float64(time.Second))
		if drift := ideal - time.Since(start); drift > 0 {
			time.Sleep(drift)
		}
	}
	_ = curFile.Close()

	fmt.Printf("ROTWRITE wrote=%d files_created=%d files_deleted=%d kept=%d bytes=%d elapsed=%s\n",
		*lines, filesCreated, filesDeleted, len(live), bytesTotal, time.Since(start).Round(time.Millisecond))
}

// taLine builds a ~200-byte ThinkingData-shaped track line with a unique #uuid.
func taLine(seq int) string {
	id := strconv.Itoa(seq)
	return `{"#type":"track","#event_name":"PaymentOrderState","#time":"2026-06-09 12:00:00.000","#uuid":"rw-` + id +
		`","#account_id":"acc-` + strconv.Itoa(seq%1000) + `","#distinct_id":"d-` + id +
		`","properties":{"seq":` + id + `,"amount":12.34,"state":"paid","sku":"item-` + strconv.Itoa(seq%50) + `"}}` + "\n"
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "rotwrite:", err)
		os.Exit(1)
	}
}
