package store

import (
	"context"
	"time"

	"rocket-nano/aura-studio/ta2mongo/config"

	"github.com/cenkalti/backoff/v4"
	"github.com/sirupsen/logrus"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MongoStore struct {
	userCollection       *mongo.Collection
	eventCollection      *mongo.Collection
	deadLetterCollection *mongo.Collection
	cfg                  config.Config
	logger               *logrus.Logger
}

func NewMongoStore(db *mongo.Database, cfg config.Config, logger *logrus.Logger) *MongoStore {
	return &MongoStore{
		userCollection:       db.Collection("user"),
		eventCollection:      db.Collection("event"),
		deadLetterCollection: db.Collection("dead_letter"),
		cfg:                  cfg,
		logger:               logger,
	}
}

func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	userIndexes := []mongo.IndexModel{
		// user upsert/query 场景：以 #account_id 为索引（不需要 #time）
		{Keys: bson.D{{Key: "#account_id", Value: 1}}},
		// user upsert/query 场景：以 #distinct_id 为索引（不需要 #time）
		{Keys: bson.D{{Key: "#distinct_id", Value: 1}}},
		// upsert 场景：以 #uuid 为唯一索引
		{
			Keys:    bson.D{{Key: "#uuid", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "_ts", Value: 1}}},
	}

	eventIndexes := []mongo.IndexModel{
		// event 查询场景 1：#event_name + #account_id + #time
		{Keys: bson.D{{Key: "#event_name", Value: 1}, {Key: "#account_id", Value: 1}, {Key: "#time", Value: 1}}},
		// event 查询场景 2：#event_name + #distinct_id + #time
		{Keys: bson.D{{Key: "#event_name", Value: 1}, {Key: "#distinct_id", Value: 1}, {Key: "#time", Value: 1}}},
		// upsert by #uuid
		{
			Keys:    bson.D{{Key: "#uuid", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
		{Keys: bson.D{{Key: "_ts", Value: 1}}},
	}

	deadLetterIndexes := []mongo.IndexModel{
		// dead-letter 查询/排查：用 _ts 避免和业务字段冲突
		{Keys: bson.D{{Key: "_ts", Value: 1}}},
	}

	if _, err := s.userCollection.Indexes().CreateMany(ctx, userIndexes); err != nil {
		return err
	}
	if _, err := s.eventCollection.Indexes().CreateMany(ctx, eventIndexes); err != nil {
		return err
	}
	if _, err := s.deadLetterCollection.Indexes().CreateMany(ctx, deadLetterIndexes); err != nil {
		return err
	}

	return nil
}

func (s *MongoStore) BuildUpsertModel(uuid string, doc bson.M) mongo.WriteModel {
	filter := bson.M{"#uuid": uuid}
	update := bson.M{"$set": doc}
	return mongo.NewUpdateOneModel().
		SetFilter(filter).
		SetUpdate(update).
		SetUpsert(true)
}

func (s *MongoStore) BuildDeadLetterModel(line string, parseErr error) mongo.WriteModel {
	// 死信集合：增加 ts 字段用于排查时间维度
	doc := bson.M{
		"_ts":   time.Now().UnixNano(),
		"line":  line,
		"error": "",
	}
	if parseErr != nil {
		doc["error"] = parseErr.Error()
	}

	return mongo.NewInsertOneModel().SetDocument(doc)
}

func (s *MongoStore) BulkWriteWithRetry(ctx context.Context, coll *mongo.Collection, models []mongo.WriteModel, logPrefix string) error {
	if len(models) == 0 {
		return nil
	}

	operation := func() error {
		opts := options.BulkWrite().SetOrdered(false)
		_, err := coll.BulkWrite(ctx, models, opts)
		return err
	}

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 200 * time.Millisecond
	bo.MaxInterval = 2 * time.Second
	bo.MaxElapsedTime = s.cfg.Retry.MaxElapsedTime
	if bo.MaxElapsedTime <= 0 {
		bo.MaxElapsedTime = 10 * time.Second
	}
	bo.Reset()

	err := backoff.Retry(operation, backoff.WithContext(bo, ctx))
	if err != nil && logPrefix != "" {
		s.logger.WithError(err).Warn(logPrefix + " (retry exhausted)")
	}
	return err
}

func (s *MongoStore) UserCollection() *mongo.Collection {
	return s.userCollection
}

func (s *MongoStore) EventCollection() *mongo.Collection {
	return s.eventCollection
}

func (s *MongoStore) DeadLetterCollection() *mongo.Collection {
	return s.deadLetterCollection
}

func (s *MongoStore) PrintStats(ctx context.Context) error {
	// Keep as-is to avoid changing behavior.
	userCount, err := s.userCollection.CountDocuments(ctx, bson.M{})
	if err != nil {
		return err
	}
	eventCount, err := s.eventCollection.CountDocuments(ctx, bson.M{})
	if err != nil {
		return err
	}

	logger := s.logger
	if logger == nil {
		logger = logrus.StandardLogger()
	}

	logger.Infof("[ta2mongo] collection %s count=%d", s.userCollection.Name(), userCount)
	logger.Infof("[ta2mongo] collection %s count=%d", s.eventCollection.Name(), eventCount)

	type idxDoc struct {
		Name string `bson:"name"`
		Key  any    `bson:"key"`
	}

	cur, err := s.userCollection.Indexes().List(ctx)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	logger.Infof("[ta2mongo] indexes for %s:", s.userCollection.Name())
	for cur.Next(ctx) {
		var doc idxDoc
		if err := cur.Decode(&doc); err != nil {
			continue
		}
		logger.Infof("  - name=%s keys=%v", doc.Name, doc.Key)
	}
	if err := cur.Err(); err != nil {
		return err
	}

	cur2, err := s.eventCollection.Indexes().List(ctx)
	if err != nil {
		return err
	}
	defer cur2.Close(ctx)

	logger.Infof("[ta2mongo] indexes for %s:", s.eventCollection.Name())
	for cur2.Next(ctx) {
		var doc idxDoc
		if err := cur2.Decode(&doc); err != nil {
			continue
		}
		logger.Infof("  - name=%s keys=%v", doc.Name, doc.Key)
	}
	return cur2.Err()
}
