package client

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/tools/tango/config"
	daomongo "rocket-nano/tools/tango/internal/dao/mongo"
	"rocket-nano/tools/tango/internal/source/tailer"
)

// UploadRequest configures a file upload (function #2: file single upload with
// retransmission). Files matching Patterns are read line-by-line and ingested
// in confirmed batches; a per-file byte-offset checkpoint is advanced after
// each confirmed batch so an interrupted upload resumes where it left off and
// any lines not yet confirmed are re-sent on the next run.
type UploadRequest struct {
	// Patterns are regex file patterns (same matching as the daemon tailer).
	Patterns []string
	// BatchSize is the number of lines per confirmed bulk write. Default 1000.
	BatchSize int
	// CheckpointCollection stores resume offsets. Defaults to
	// config.DefaultFileUploadCheckpointCollection.
	CheckpointCollection string
}

// UploadResult summarises a file upload run.
type UploadResult struct {
	Files       int   `json:"files"`
	Lines       int64 `json:"lines"`
	ResumedFrom int64 `json:"resumedFromBytes"`
}

// fileCheckpoint is the resume document for one file, keyed by absolute path.
type fileCheckpoint struct {
	Path      string    `bson:"_id"`
	Offset    int64     `bson:"offset"`
	Size      int64     `bson:"size"`
	UpdatedAt time.Time `bson:"updatedAt"`
}

// UploadFiles performs a resumable file upload. It is safe to re-run: confirmed
// lines below each file's checkpoint are skipped, so re-running after a crash
// re-sends only the lines that were never confirmed.
func (c *Client) UploadFiles(ctx context.Context, req UploadRequest) (UploadResult, error) {
	if len(req.Patterns) == 0 {
		return UploadResult{}, fmt.Errorf("client: UploadFiles: at least one pattern is required")
	}
	batchSize := req.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	collName := req.CheckpointCollection
	if collName == "" {
		collName = config.DefaultFileUploadCheckpointCollection
	}
	dbName, err := daomongo.MongoDBFromURI(c.opts.URI)
	if err != nil {
		return UploadResult{}, fmt.Errorf("client: %w", err)
	}
	ckpt := c.client.Database(dbName).Collection(collName)

	files := tailer.DiscoverFiles(req.Patterns)
	var res UploadResult
	res.Files = len(files)
	for _, path := range files {
		n, resumed, err := c.uploadOneFile(ctx, ckpt, path, batchSize)
		if err != nil {
			return res, fmt.Errorf("client: upload %q: %w", path, err)
		}
		res.Lines += n
		res.ResumedFrom += resumed
	}
	return res, nil
}

func (c *Client) uploadOneFile(ctx context.Context, ckpt *mongo.Collection, path string, batchSize int) (int64, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return 0, 0, err
	}

	// Load the resume offset; reset to 0 if the file shrank (rotation/truncate).
	offset := loadCheckpointOffset(ctx, ckpt, path)
	if info.Size() < offset {
		offset = 0
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return 0, 0, err
		}
	}
	resumedFrom := offset

	reader := bufio.NewReader(f)
	var (
		batch     []string
		processed int64
		consumed  = offset
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := c.IngestBatch(ctx, batch); err != nil {
			return err
		}
		processed += int64(len(batch))
		batch = batch[:0]
		return saveCheckpointOffset(ctx, ckpt, path, consumed, info.Size())
	}

	for {
		line, err := reader.ReadString('\n')
		consumed += int64(len(line))
		if trimmed := trimNewline(line); trimmed != "" {
			batch = append(batch, trimmed)
			if len(batch) >= batchSize {
				if ferr := flush(); ferr != nil {
					return processed, resumedFrom, ferr
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return processed, resumedFrom, err
		}
	}
	if ferr := flush(); ferr != nil {
		return processed, resumedFrom, ferr
	}
	return processed, resumedFrom, nil
}

func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

func loadCheckpointOffset(ctx context.Context, coll *mongo.Collection, path string) int64 {
	var cp fileCheckpoint
	if err := coll.FindOne(ctx, bson.M{"_id": path}).Decode(&cp); err != nil {
		return 0
	}
	return cp.Offset
}

func saveCheckpointOffset(ctx context.Context, coll *mongo.Collection, path string, offset, size int64) error {
	_, err := coll.UpdateOne(ctx,
		bson.M{"_id": path},
		bson.M{"$set": bson.M{"offset": offset, "size": size, "updatedAt": time.Now().UTC()}},
		options.Update().SetUpsert(true),
	)
	return err
}
