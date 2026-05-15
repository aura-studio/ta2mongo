package runner

import (
	"context"
	"sync"
	"time"

	"github.com/hpcloud/tail"
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"rocket-nano/aura-studio/ta2mongo/config"
	"rocket-nano/aura-studio/ta2mongo/matches"
	"rocket-nano/aura-studio/ta2mongo/parser"
	"rocket-nano/aura-studio/ta2mongo/store"
)

var UserTypes = map[string]struct{}{
	"user_set":     {},
	"user_unset":   {},
	"user_setOnce": {},
	"user_add":     {},
	"user_append":  {},
	"user_del":     {},
}

var EventTypes = map[string]struct{}{
	"track":           {},
	"track_update":    {},
	"track_overwrite": {},
}

type Runner struct {
	cfg    config.Config
	logger *logrus.Logger
	store  *store.MongoStore
	parser *parser.Parser
}

func NewRunner(ctx context.Context, cfg config.Config, logger *logrus.Logger) (*Runner, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.Mongo.URI))
	if err != nil {
		return nil, err
	}

	// keep client lifetime bound to ctx; we disconnect on ctx.Done()
	go func() {
		<-ctx.Done()
		_ = client.Disconnect(context.Background())
	}()

	db := client.Database(cfg.Mongo.DB)
	st := store.NewMongoStore(db, cfg, logger)
	p := parser.NewParser()

	return &Runner{
		cfg:    cfg,
		logger: logger,
		store:  st,
		parser: p,
	}, nil
}

func (r *Runner) EnsureIndexes(ctx context.Context) error {
	// index.ensureIndexes 现在已固定开启（配置项已删除），因此始终执行。
	return r.store.EnsureIndexes(ctx)
}

func (r *Runner) PrintStats(ctx context.Context) error {
	return r.store.PrintStats(ctx)
}

func (r *Runner) RunDaemon(ctx context.Context) error {
	lineCh := make(chan string, 2000)

	// start source; closes lineCh when ctx done
	go func() {
		_ = r.startDaemonSource(ctx, lineCh)
		close(lineCh)
	}()

	workerCount := r.cfg.Batch.WorkerCount
	if workerCount <= 0 {
		workerCount = 1
	}

	var wg sync.WaitGroup
	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go func() {
			defer wg.Done()
			r.workerDaemon(ctx, lineCh)
		}()
	}

	wg.Wait()
	return nil
}

func (r *Runner) startDaemonSource(ctx context.Context, out chan<- string) error {
	// daemon-only: always start from file end (incremental consumption)
	seekInfo := &tail.SeekInfo{Whence: 2, Offset: 0}

	tailed := make(map[string]struct{})
	tails := make(map[string]*tail.Tail)
	var mu sync.Mutex

	startFile := func(path string) {
		mu.Lock()
		if _, ok := tailed[path]; ok {
			mu.Unlock()
			return
		}
		tailed[path] = struct{}{}
		mu.Unlock()

		tt, err := tail.TailFile(path, tail.Config{
			Location:    seekInfo,
			ReOpen:      true,
			Follow:      true,
			MustExist:   false,
			Poll:        false,
			Logger:      tail.DiscardingLogger,
			MaxLineSize: 1024 * 1024,
		})
		if err != nil {
			r.logger.WithError(err).WithField("path", path).Warn("tail start failed")
			mu.Lock()
			delete(tailed, path)
			mu.Unlock()
			return
		}

		mu.Lock()
		tails[path] = tt
		mu.Unlock()

		go func(t *tail.Tail) {
			defer func() { _ = t.Stop() }()
			for {
				select {
				case <-ctx.Done():
					return
				case line, ok := <-t.Lines:
					if !ok {
						return
					}
					if line == nil {
						continue
					}
					text := line.Text
					// keep the previous behavior: trim+skip empty
					// (avoid importing strings here by doing a cheap check)
					// If you want full trim, switch to strings.TrimSpace.
					if len(text) == 0 {
						continue
					}

					select {
					case out <- text:
					case <-ctx.Done():
						return
					}
				}
			}
		}(tt)
	}

	rescanOnce := func() {
		for _, m := range matches.CollectMatches(r.cfg.Ta.LogPattern) {
			startFile(m)
		}
	}

	// initial scan
	rescanOnce()

	// tail.rescan 一定会开启：因此直接按 rescanSeconds 周期重扫，直到 ctx.Done()
	ticker := time.NewTicker(time.Duration(r.cfg.Tail.RescanSeconds) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			mu.Lock()
			for _, t := range tails {
				if t != nil {
					_ = t.Stop()
				}
			}
			mu.Unlock()
			return nil
		case <-ticker.C:
			rescanOnce()
		}
	}
}

func (r *Runner) workerDaemon(ctx context.Context, lineCh <-chan string) {
	userBatch := make([]mongo.WriteModel, 0, r.cfg.Batch.Size)
	eventBatch := make([]mongo.WriteModel, 0, r.cfg.Batch.Size)

	deadLetterBatch := make([]mongo.WriteModel, 0, 128)

	lastFlush := time.Now()
	flush := func() {
		if len(userBatch) > 0 {
			if err := r.store.BulkWriteWithRetry(ctx, r.store.UserCollection(), userBatch, "[ta2mongo] bulkWrite user failed"); err != nil {
				r.logger.WithError(err).Error("[ta2mongo] bulkWrite user failed")
			}
		}
		if len(eventBatch) > 0 {
			if err := r.store.BulkWriteWithRetry(ctx, r.store.EventCollection(), eventBatch, "[ta2mongo] bulkWrite event failed"); err != nil {
				r.logger.WithError(err).Error("[ta2mongo] bulkWrite event failed")
			}
		}
		userBatch = userBatch[:0]
		eventBatch = eventBatch[:0]
		lastFlush = time.Now()
	}

	flushDeadLetters := func() {
		if len(deadLetterBatch) == 0 {
			return
		}
		if err := r.store.BulkWriteWithRetry(ctx, r.store.DeadLetterCollection(), deadLetterBatch, "[ta2mongo] bulkWrite dead_letter failed"); err != nil {
			r.logger.WithError(err).Error("[ta2mongo] bulkWrite dead_letter failed")
		}
		deadLetterBatch = deadLetterBatch[:0]
	}

	invalidCount := 0
	for line := range lineCh {
		rec, err := r.parser.ParseLine(line)
		if err != nil {
			invalidCount++
			if invalidCount%1000 == 0 {
				r.logger.WithError(err).Warnf("[ta2mongo] drop invalid log line (invalidCount=%d)", invalidCount)
			}

			// persist the raw line for troubleshooting
			deadLetterBatch = append(deadLetterBatch, r.store.BuildDeadLetterModel(line, err))
			if len(deadLetterBatch) >= 100 {
				flushDeadLetters()
			}
			continue
		}

		if _, ok := UserTypes[rec.Type]; ok {
			userBatch = append(userBatch, r.store.BuildUpsertModel(rec.UUID, rec.Doc))
		} else if _, ok := EventTypes[rec.Type]; ok {
			eventBatch = append(eventBatch, r.store.BuildUpsertModel(rec.UUID, rec.Doc))
		} else {
			eventBatch = append(eventBatch, r.store.BuildUpsertModel(rec.UUID, rec.Doc))
		}

		flushBySize := len(userBatch) >= r.cfg.Batch.Size || len(eventBatch) >= r.cfg.Batch.Size
		flushByTime := time.Since(lastFlush) >= time.Duration(r.cfg.Batch.FlushIntervalMs)*time.Millisecond
		if flushBySize || flushByTime {
			flush()
		}

		select {
		case <-ctx.Done():
			flush()
			flushDeadLetters()
			return
		default:
		}
	}

	flush()
	flushDeadLetters()
}
